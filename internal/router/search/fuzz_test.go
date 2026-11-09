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

package search

import (
	"math"
	"strings"
	"testing"
	"unicode"
)

// FuzzTokens checks that tokens are non-empty, lower-case runs of letters and digits.
func FuzzTokens(f *testing.F) {
	for _, seed := range []string{"listPullRequests", "get_file-contents.v2", "HTTPServer", "ÄÖü ßtraße", "", "  ", "a1B2"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, text string) {
		for _, token := range Tokens(text) {
			if token == "" {
				t.Fatalf("Tokens(%q) produced an empty token", text)
			}

			if token != strings.ToLower(token) {
				t.Fatalf("Tokens(%q) produced %q, which is not lower case", text, token)
			}

			for _, char := range token {
				if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
					t.Fatalf("Tokens(%q) produced %q containing %q", text, token, char)
				}
			}
		}
	})
}

// FuzzSearch checks that any query yields at most limit valid hits with finite scores, best first.
func FuzzSearch(f *testing.F) {
	index := Build([]Document{
		{
			Server:            "github",
			ServerDescription: "GitHub repositories and issues.",
			Tool:              "list_pull_requests",
			Description:       "List pull requests in a repository.",
		},
		{Server: "github", ServerDescription: "GitHub repositories and issues.", Tool: "create_issue", Description: "Open a new issue."},
		{Server: "files", ServerDescription: "Local files.", Tool: "read_file", Description: "Read a file from /srv/data."},
		{Server: "browser", Tool: "navigate", Description: ""},
	})

	for _, seed := range []string{"list open pull requests", "read /srv/data/x.txt", "create_issue", "", "the a of", "ПРИВЕТ"} {
		f.Add(seed, uint8(3), false)
	}

	f.Fuzz(func(t *testing.T, query string, limit uint8, onlyGitHub bool) {
		var servers []string
		if onlyGitHub {
			servers = []string{"github"}
		}

		hits := index.Search(query, servers, int(limit))
		if len(hits) > int(limit) {
			t.Fatalf("Search(%q, limit %d) returned %d hits", query, limit, len(hits))
		}

		for position, hit := range hits {
			if hit.Index < 0 || hit.Index >= len(index.documents) {
				t.Fatalf("hit %d has index %d out of range", position, hit.Index)
			}

			if onlyGitHub && index.documents[hit.Index].Server != "github" {
				t.Fatalf("hit %d is from server %q despite the filter", position, index.documents[hit.Index].Server)
			}

			if math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) || hit.Score <= 0 {
				t.Fatalf("hit %d has score %v", position, hit.Score)
			}

			if position > 0 && hit.Score > hits[position-1].Score {
				t.Fatalf("hits are not sorted: %v", hits)
			}
		}
	})
}
