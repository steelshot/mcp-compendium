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

// TestSearchRanksNamedTools checks that a query stating a tool's name finds it among tools with shared prefixes and
// descriptions of very different lengths, with and without a gateway that adds another prefix.
func TestSearchRanksNamedTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query   string
		servers []string
		want    string
	}{
		{query: "get a Jira issue by key", want: "jira_get_issue"},
		{query: "jira get issue", servers: []string{"jira"}, want: "jira_get_issue"},
		{query: "get issue", servers: []string{"jira"}, want: "jira_get_issue"},
		{query: "transition an issue to done", want: "jira_transition_issue"},
		{query: "search jira issues with JQL", want: "jira_search"},
	}

	for _, prefix := range []string{"", "atlassian-"} {
		documents := append([]Document{
			{Server: "github", Tool: "create_issue", Description: "Create a new issue in a repository."},
			{Server: "github", Tool: "list_issues", Description: "List issues in a repository."},
			{Server: "github", Tool: "add_issue_comment", Description: "Add a comment to an issue."},
		}, jiraCatalogue(prefix)...)

		index := Build(documents)

		for _, test := range cases {
			hits := index.Search(test.query, test.servers, 5)
			if len(hits) > 0 && documents[hits[0].Index].Tool == prefix+test.want {
				continue
			}

			names := make([]string, 0, len(hits))
			for _, hit := range hits {
				names = append(names, documents[hit.Index].Tool)
			}

			t.Errorf("%q with prefix %q: top hits %v, want %s", test.query, prefix, names, prefix+test.want)
		}
	}
}

func TestCoreNamesDropSharedPrefix(t *testing.T) {
	t.Parallel()

	documents := []Document{
		{Server: "gateway", Tool: "corp-jira_get_issue"},
		{Server: "gateway", Tool: "corp-jira_search"},
		{Server: "gateway", Tool: "corp-jira"},
		{Server: "files", Tool: "searchFiles"},
		{Server: "browser", Tool: "browser_navigate"},
	}

	names := coreNames(documents)

	want := []string{"getissue", "search", "jira", "searchfiles", "browsernavigate"}
	for position, name := range names {
		if name.text != want[position] {
			t.Errorf("%s: core %q, want %q", documents[position].Tool, name.text, want[position])
		}
	}

	if !slices.Equal(names[0].terms, []string{"get", "issu"}) || !slices.Equal(names[3].terms, []string{"search", "fil"}) {
		t.Errorf("terms = %v, %v", names[0].terms, names[3].terms)
	}

	// A shortened name of one word does not count as named: "search" must not rank jira_search above searchFiles.
	documents[3].Description = "Search for files by name."
	if hits := Build(documents).Search("search files", nil, 1); len(hits) == 0 || hits[0].Index != 3 {
		t.Errorf("search files: hits %v, want searchFiles first", hits)
	}
}

// jiraCatalogue models a real issue tracker's MCP server: every name starts with jira_, nearly every tool mentions
// issues and keys, and the most used tools document each parameter, so their descriptions are many times longer than
// those of the tools around them.
func jiraCatalogue(prefix string) []Document {
	const server = "jira"

	tools := []struct{ name, description string }{
		{"get_issue", `Get the details of one issue.

Returns the issue's fields, and optionally its comments, links, transitions, watchers and change history, so that
separate calls are not needed. For an epic, the response can also summarise its child issues by status and assignee.

Args:
    issue_key: The issue key, such as PROJ-123.
    fields: Comma-separated field names to return, or *all for every field.
    expand: Comma-separated sections to expand, such as renderedFields or changelog.
    comment_limit: The maximum number of comments to include.
    properties: Comma-separated issue properties to return.
    update_history: Whether to record the view in the user's recently viewed issues.
    include: Comma-separated enrichments to inline: links, transitions, watchers, changelog, comments, worklogs.

Returns:
    The issue as JSON, with its key, summary, status, assignee, reporter, priority, labels and requested sections.`},
		{"get_issue_watchers", "Get the users watching an issue."},
		{"get_issue_dates", "Get an issue's created, updated and due dates and its status history."},
		{"get_worklog", "Get the work logged on an issue."},
		{"get_transitions", "Get the status transitions available for an issue."},
		{"get_project_issues", "Get every issue in a project."},
		{"get_sprint_issues", "Get the issues in a sprint."},
		{"get_board_issues", "Get the issues on a board, filtered by JQL."},
		{"get_agile_boards", "Get agile boards by name, project key or type."},
		{"get_link_types", "Get the available issue link types."},
		{"get_all_projects", "Get every project the user can see."},
		{"get_service_desk_for_project", "Get the service desk for a project key."},
		{"search_assignable_users", "Search the users who can be assigned issues in a project, by name or key."},
		{"search_fields", "Search issue fields by keyword."},
		{"search", `Search issues with a JQL query.

Args:
    jql: The JQL query, such as project = PROJ AND status = Open.
    fields: Comma-separated field names to return.
    limit: The maximum number of issues to return.
    start_at: The index of the first issue to return, for paging.

Returns:
    The matching issues as JSON, with their keys, summaries and the requested fields.`},
		{"transition_issue", `Move an issue to a new status.

Args:
    issue_key: The issue key, such as PROJ-123.
    transition_id: The transition to perform; get_transitions lists them.
    fields: Fields to set during the transition, as JSON.
    comment: A comment to add with the transition.`},
		{"add_comment", `Add a comment to an issue.

Args:
    issue_key: The issue key, such as PROJ-123.
    body: The comment text, in Markdown.
    visibility: Restrict the comment to a group or role.`},
		{"create_issue", `Create an issue.

Args:
    project_key: The project key, such as PROJ.
    summary: The issue summary.
    issue_type: The issue type, such as Task, Bug or Story.
    description: The issue description, in Markdown.
    assignee: The assignee's account ID or email address.
    components: Comma-separated component names.
    additional_fields: Other fields to set, as JSON, such as priority, labels or a parent issue key.`},
		{"update_issue", `Update an issue's fields, and optionally transition it, comment on it or log work.

Args:
    issue_key: The issue key, such as PROJ-123.
    fields: The fields to update, as JSON.
    additional_fields: Other fields to update, as JSON.
    attachments: Paths of files to attach.`},
	}

	documents := make([]Document, 0, len(tools))
	for _, tool := range tools {
		documents = append(documents, Document{
			Server: server, ServerDescription: "Jira issues, boards and sprints.", Tool: prefix + "jira_" + tool.name, Description: tool.description,
		})
	}

	return documents
}
