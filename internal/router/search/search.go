/*
 * Copyright (c) 2026 Džiugas Eiva
 *
 * SPDX-License-Identifier: AGPL-3.0-only
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

// Package search ranks tools against an agent's free-text intent with BM25 over tool names, tool descriptions and
// server names and descriptions. It is lexical and in-process: no embeddings, models or network calls.
package search

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode"
)

// Document is one searchable tool.
type Document struct {
	Server            string
	ServerDescription string
	Tool              string
	Description       string
}

// Hit is a ranked result; Index refers to the document's position in the slice passed to Build.
type Hit struct {
	Index int
	Score float64
}

// relevance separates a document's own BM25 score from the bonus contributed by its server description.
type relevance struct {
	own     float64
	context float64
}

// Index is an immutable BM25 index.
type Index struct {
	documents []Document
	terms     []map[string]float64
	context   []map[string]bool
	lengths   []float64
	average   float64
	frequency map[string]int
}

// Field weights: a match in the tool name is far stronger evidence than one in a long description.
const (
	weightToolName    = 3.0
	weightServerName  = 2.0
	weightDescription = 1.0
	// contextBonus is added per query term found only in the server's description, enough to break ties.
	contextBonus = 0.2

	k1 = 1.2
	b  = 0.75

	minStemLength = 4

	// leadWeight multiplies the first content word, usually the verb of an instruction such as "list open pull
	// requests", which says more about the wanted tool than the words after it.
	leadWeight = 2.0
	// hintWeight is the weight of terms implied by entities in the query rather than written in it.
	hintWeight = 0.5
)

//nolint:gochecknoglobals,goconst // fixed vocabularies
var (
	stopwords = map[string]bool{
		"a": true, "an": true, "and": true, "any": true, "are": true, "as": true, "at": true, "be": true, "by": true,
		"can": true, "do": true, "for": true, "from": true, "i": true, "in": true, "into": true, "is": true,
		"it": true, "me": true, "my": true, "of": true, "on": true, "or": true, "please": true, "some": true,
		"that": true, "the": true, "this": true, "to": true, "tool": true, "tools": true, "use": true, "using": true,
		"want": true, "with": true, "you": true, "how": true, "what": true, "which": true, "when": true, "where": true,
		"why": true, "was": true, "were": true, "does": true, "did": true, "has": true, "have": true, "will": true,
		"would": true, "could": true, "should": true, "its": true, "about": true,
	}

	// synonyms expands common agent vocabulary onto the terms tool authors tend to use. Keep it small and generic.
	synonyms = map[string][]string{
		"ticket":    {"issue"},
		"bug":       {"issue"},
		"pr":        {"pull", "request"},
		"mr":        {"merge", "request"},
		"repo":      {"repository"},
		"remove":    {"delete"},
		"delete":    {"remove"},
		"find":      {"search"},
		"lookup":    {"search", "get"},
		"fetch":     {"get", "read"},
		"show":      {"get", "list"},
		"read":      {"get"},
		"open":      {"create", "read", "get"},
		"edit":      {"update", "modify"},
		"change":    {"update"},
		"add":       {"create"},
		"new":       {"create"},
		"make":      {"create"},
		"dir":       {"directory"},
		"folder":    {"directory"},
		"file":      {"files"},
		"doc":       {"document"},
		"docs":      {"documentation", "document"},
		"browse":    {"navigate"},
		"go":        {"navigate"},
		"visit":     {"navigate"},
		"big":       {"size"},
		"large":     {"size"},
		"small":     {"size"},
		"size":      {"big"},
		"structure": {"tree"},
		"website":   {"page", "url"},
		"web":       {"page", "url"},
		"run":       {"execute"},
		"exec":      {"execute"},
		"comment":   {"comments"},
	}
)

// Build indexes documents.
func Build(documents []Document) *Index {
	index := &Index{
		documents: documents,
		terms:     make([]map[string]float64, len(documents)),
		context:   make([]map[string]bool, len(documents)),
		lengths:   make([]float64, len(documents)),
		frequency: make(map[string]int),
	}

	total := 0.0

	for position, document := range documents {
		terms := make(map[string]float64)
		addTerms(terms, document.Tool, weightToolName)
		addTerms(terms, document.Server, weightServerName)
		addTerms(terms, document.Description, weightDescription)

		context := make(map[string]float64)
		addTerms(context, document.ServerDescription, 1)

		length := 0.0
		for term, weight := range terms {
			length += weight
			index.frequency[term]++
		}

		index.terms[position] = terms
		index.context[position] = make(map[string]bool, len(context))

		for term := range context {
			index.context[position][term] = true
		}
		index.lengths[position] = length
		total += length
	}

	if len(documents) > 0 {
		index.average = total / float64(len(documents))
	}

	return index
}

// Tokens splits text into lower-case terms, breaking camelCase, snake_case, kebab-case and dotted names apart.
func Tokens(text string) []string {
	words := make([]string, 0)

	var current []rune

	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = current[:0]
		}
	}

	// mark records where the current separator-delimited word started; when case changes split it into several
	// parts, the whole word is added too.
	wordStart, wordText := 0, []rune(nil)
	mark := func() {
		if len(words)-wordStart > 1 {
			words = append(words, strings.ToLower(string(wordText)))
		}

		wordStart, wordText = len(words), wordText[:0]
	}

	runes := []rune(text)
	for position, char := range runes {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			flush()
			mark()
		case unicode.IsUpper(char) && position > 0 && (unicode.IsLower(runes[position-1]) ||
			(position+1 < len(runes) && unicode.IsUpper(runes[position-1]) && unicode.IsLower(runes[position+1]))):
			flush()

			current = append(current, char)
		default:
			current = append(current, char)
		}

		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			wordText = append(wordText, char)
		}
	}

	flush()
	mark()

	return words
}

// Search returns up to limit hits for query, best first. When servers is non-empty only those servers are searched.
func (i *Index) Search(query string, servers []string, limit int) []Hit {
	terms := i.queryTerms(query)
	if len(terms) == 0 || limit <= 0 {
		return nil
	}

	count := float64(len(i.documents))
	normalizedQuery := normalizeName(query)
	hits := make([]Hit, 0)

	for position, document := range i.documents {
		if len(servers) > 0 && !slices.Contains(servers, document.Server) {
			continue
		}

		scored := i.score(position, terms, count)

		// The agent named the tool outright; rank it first.
		if name := normalizeName(document.Tool); name != "" && strings.Contains(normalizedQuery, name) {
			scored.own += 10
		}

		// Server descriptions add context but never make a tool a hit on their own.
		if scored.own > 0 {
			hits = append(hits, Hit{Index: position, Score: scored.own + scored.context})
		}
	}

	slices.SortStableFunc(hits, func(a, b Hit) int { return cmp.Compare(b.Score, a.Score) })

	if len(hits) > limit {
		hits = hits[:limit]
	}

	return hits
}

func addTerms(terms map[string]float64, text string, weight float64) {
	for _, word := range Tokens(text) {
		if stopwords[word] {
			continue
		}

		terms[stem(word)] += weight
	}
}

// looksLikePath reports whether a query word names a file or directory: an absolute or home-relative path, or a name
// with a short extension such as config.yaml.
func looksLikePath(field string) bool {
	field = strings.TrimRight(field, ".,;:!?")
	if strings.HasPrefix(field, "/") || strings.HasPrefix(field, "./") || strings.HasPrefix(field, "~/") {
		return true
	}

	dot := strings.LastIndexByte(field, '.')
	if dot <= 0 || len(field)-dot-1 < 1 || len(field)-dot-1 > 5 {
		return false
	}

	for _, char := range field[dot+1:] {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return false
		}
	}

	return true
}

// stem strips common English plural, verb and adverb suffixes so "issues" matches "issue", "listing" matches "list",
// "modified" and "modification" match "modify", and "recursively" matches "recursive". It only has to map related
// words onto the same root, not produce real words.
func stem(word string) string {
	if len(word) < minStemLength {
		return word
	}

	return trimE(stripSuffix(word))
}

func stripSuffix(word string) string {
	for _, suffix := range []string{"ication", "ies", "ied", "ing", "ly", "es", "ed", "s"} {
		if !strings.HasSuffix(word, suffix) || len(word)-len(suffix) < 3 {
			continue
		}

		root := strings.TrimSuffix(word, suffix)

		switch suffix {
		case "ication", "ies", "ied":
			return root + "y"
		case "s":
			if strings.HasSuffix(root, "s") || strings.HasSuffix(root, "u") {
				return word
			}
		}

		return root
	}

	return word
}

// trimE drops a final "e" so that "name", "names" and "named" share the root "nam".
func trimE(word string) string {
	if len(word) >= minStemLength && strings.HasSuffix(word, "e") {
		return word[:len(word)-1]
	}

	return word
}

func normalizeName(text string) string {
	return strings.Join(Tokens(text), "")
}

// score returns the BM25 score of the document's own fields and the bonus from its server description.
func (i *Index) score(position int, terms map[string]float64, count float64) relevance {
	score, bonus := 0.0, 0.0

	for term, queryWeight := range terms {
		frequency, ok := i.terms[position][term]
		if !ok {
			if i.context[position][term] {
				bonus += contextBonus * queryWeight
			}

			continue
		}

		documents := float64(i.frequency[term])
		idf := math.Log(1 + (count-documents+0.5)/(documents+0.5))
		norm := frequency * (k1 + 1) / (frequency + k1*(1-b+b*i.lengths[position]/i.average))
		score += queryWeight * idf * norm
	}

	return relevance{own: score, context: bonus}
}

// queryTerms weights the query's own words at 1, and the first of them at leadWeight. Synonyms translate the agent's
// vocabulary into the tools': they take the word's full weight when the word itself never occurs in the index, and
// half of it when it does. Entities add hint terms: a URL implies "url", a file path implies "file".
func (i *Index) queryTerms(query string) map[string]float64 {
	terms := make(map[string]float64)
	lead := true

	for _, word := range Tokens(query) {
		if stopwords[word] {
			continue
		}

		weight := 1.0
		if lead {
			weight, lead = leadWeight, false
		}

		stemmed := stem(word)
		terms[stemmed] = max(terms[stemmed], weight)

		synonymWeight := weight / 2
		if i.frequency[stemmed] == 0 {
			synonymWeight = weight
		}

		for _, synonym := range synonyms[word] {
			terms[stem(synonym)] = max(terms[stem(synonym)], synonymWeight)
		}
	}

	for field := range strings.FieldsSeq(query) {
		switch {
		case strings.HasPrefix(field, "http://"), strings.HasPrefix(field, "https://"):
			terms["url"] = max(terms["url"], 1)
		case looksLikePath(field):
			terms["file"] = max(terms["file"], hintWeight)
		}
	}

	return terms
}
