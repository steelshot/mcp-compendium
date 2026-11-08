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
	"slices"
	"testing"
)

func TestTokens(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"createPullRequest":       {"create", "pull", "request", "createpullrequest"},
		"get_issue-comments.v2":   {"get", "issue", "comments", "v2"},
		"HTTPServerStatus":        {"http", "server", "status", "httpserverstatus"},
		"GitHub repos":            {"git", "hub", "github", "repos"},
		"browser_navigate":        {"browser", "navigate"},
		"Read the README, please": {"read", "the", "readme", "please"},
	}

	for input, want := range cases {
		if got := Tokens(input); !slices.Equal(got, want) {
			t.Errorf("Tokens(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestStemGroupsRelatedWords(t *testing.T) {
	t.Parallel()

	groups := [][]string{
		{"issue", "issues"},
		{"repository", "repositories"},
		{"branch", "branches"},
		{"file", "files"},
		{"list", "lists", "listing"},
		{"page", "pages"},
		{"modify", "modified", "modification"},
		{"recursive", "recursively"},
		{"copy", "copied"},
		{"name", "names", "named"},
		{"create", "created", "creates"},
		{"update", "updated", "updates"},
	}

	for _, group := range groups {
		for _, word := range group[1:] {
			if stem(word) != stem(group[0]) {
				t.Errorf("stem(%q) = %q, but stem(%q) = %q", word, stem(word), group[0], stem(group[0]))
			}
		}
	}

	for _, word := range []string{"status", "address", "run"} {
		if stem(word) != word {
			t.Errorf("stem(%q) = %q, want it unchanged", word, stem(word))
		}
	}
}

func TestSearchRanksIntent(t *testing.T) {
	t.Parallel()

	documents := []Document{
		{Server: "github", ServerDescription: "GitHub repositories and issues.", Tool: "create_issue", Description: "Create a new issue in a repository."},
		{Server: "github", ServerDescription: "GitHub repositories and issues.", Tool: "list_issues", Description: "List issues in a repository."},
		{Server: "github", ServerDescription: "GitHub repositories and issues.", Tool: "create_pull_request", Description: "Open a pull request."},
		{Server: "github", ServerDescription: "GitHub repositories and issues.", Tool: "delete_file", Description: "Delete a file from a branch."},
		{Server: "browser", ServerDescription: "Browser automation.", Tool: "browser_navigate", Description: "Navigate to a URL."},
		{Server: "files", ServerDescription: "Local files.", Tool: "read_file", Description: "Read the contents of a file."},
	}
	index := Build(documents)

	cases := []struct {
		query   string
		servers []string
		want    string
	}{
		{query: "open a new ticket", want: "create_issue"},
		{query: "make a PR", want: "create_pull_request"},
		{query: "show me the issues", want: "list_issues"},
		{query: "go to a website", want: "browser_navigate"},
		{query: "read_file", want: "read_file"},
		{query: "remove a file", want: "delete_file"},
		{query: "read a file", servers: []string{"files"}, want: "read_file"},
	}

	for _, test := range cases {
		hits := index.Search(test.query, test.servers, 3)
		if len(hits) == 0 {
			t.Errorf("%q: no hits", test.query)

			continue
		}

		if got := documents[hits[0].Index].Tool; got != test.want {
			t.Errorf("%q: top hit %s, want %s", test.query, got, test.want)
		}

		for _, hit := range hits {
			if len(test.servers) > 0 && !slices.Contains(test.servers, documents[hit.Index].Server) {
				t.Errorf("%q: hit from filtered-out server %s", test.query, documents[hit.Index].Server)
			}
		}
	}

	if hits := index.Search("the a of", nil, 5); hits != nil {
		t.Errorf("stopword-only query returned %v", hits)
	}

	if hits := index.Search("repositories", []string{"browser"}, 5); len(hits) != 0 {
		t.Errorf("server-description-only match returned %v", hits)
	}

	if hits := index.Search("quantum entanglement", nil, 5); len(hits) != 0 {
		t.Errorf("unrelated query returned %v", hits)
	}
}
