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

package router

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/testmcp"
)

// fakePool serves fixed snapshots and records calls.
type fakePool struct {
	mu        sync.Mutex
	snapshots []downstream.Snapshot
	listeners []func()
	result    *mcp.CallToolResult
	err       error
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()

	github := testmcp.ServeHTTP(t, testmcp.NewServer(
		"github",
		"",
		"",
		testmcp.Tool{
			Name: "create_issue", Description: "Create a new issue in a repository. Supports labels.",
			Properties: map[string]string{"owner": "string", "repo": "string", "title": "string", "body": "string"},
			Required:   []string{"owner", "repo", "title"}, Destructive: true,
		},
		testmcp.Tool{Name: "list_issues", Description: "List issues.", ReadOnly: true, Properties: map[string]string{"repo": "string"}},
		testmcp.Tool{Name: "delete_repo", Description: "Delete a repository."},
		testmcp.Tool{
			Name:        "structured",
			Description: "Return structured output.",
			Handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content:           []mcp.Content{&mcp.TextContent{Text: "{\n  \"count\": 2\n}"}},
					StructuredContent: map[string]any{"count": 2},
				}, nil
			},
		},
		testmcp.Tool{
			Name:        "huge",
			Description: "Return a lot of text.",
			Handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
				return testmcp.Text(strings.Repeat("x", 5000)), nil
			},
		},
	))

	servers := []*config.Server{
		{
			Name: "github", Transport: config.TransportHTTP, Enabled: true, URL: mustURL(t, github),
			Description: "GitHub repositories and issues.", Tools: config.ToolFilter{Exclude: []string{"delete_*"}},
			Limits: config.Limits{MaxResultBytes: 2048},
		},
	}

	pool := downstream.New(servers, downstream.Settings{StartupTimeout: 10 * time.Second, ExecutionTimeout: 10 * time.Second}, nil, logging.Discard())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); pool.Close() })

	r := New(Settings{MaxResultBytes: DefMaxResultBytes}, pool, logging.Discard())
	pool.Start(ctx)

	if err := pool.WaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}

	session := connect(t, r)

	tools := listTools(t, session)
	if len(tools) != 3 || !strings.Contains(tools[toolSearch].Description, "- github (4 tools) - GitHub repositories and issues.") {
		t.Fatalf("tools/list = %v\n%s", keys(tools), tools[toolSearch].Description)
	}

	t.Run("search", func(t *testing.T) {
		t.Parallel()

		text, isError := call(t, session, toolSearch, map[string]any{"query": "open a ticket"})
		want := "github create_issue(owner: string, repo: string, title: string, body?: string) [destructive] - Create a new issue in a repository."
		if isError || !strings.HasPrefix(text, want) {
			t.Errorf("search = %q", text)
		}

		if text, _ = call(t, session, toolSearch, map[string]any{"query": "quantum flux"}); !strings.Contains(text, "No tools matched") {
			t.Errorf("empty search = %q", text)
		}
	})

	t.Run("describe", func(t *testing.T) {
		t.Parallel()

		text, isError := call(
			t,
			session,
			toolDescribe,
			map[string]any{"tools": []map[string]string{{"server": "github", "tool": "list_issues"}, {"server": "github", "tool": "nope"}}},
		)

		var payload struct {
			Tools  []describedTool `json:"tools"`
			Errors []toolError     `json:"errors"`
		}
		if err := json.Unmarshal([]byte(text), &payload); isError || err != nil {
			t.Fatalf("describe = %q, %v", text, err)
		}

		if len(payload.Tools) != 1 || payload.Tools[0].Hints[0] != "read-only" || payload.Tools[0].InputSchema == nil ||
			payload.Errors[0].Code != codeUnknownTool {
			t.Errorf("describe payload = %+v", payload)
		}

		listing, _ := call(t, session, toolDescribe, map[string]any{"server": "github"})
		if !strings.Contains(listing, "list_issues(repo?: string) [read-only] - List issues.") || strings.Contains(listing, "delete_repo") {
			t.Errorf("listing = %q", listing)
		}
	})

	t.Run("call", func(t *testing.T) {
		t.Parallel()

		args := map[string]any{"owner": "o", "repo": "r", "title": "t"}
		if text, isError := call(
			t,
			session,
			toolCall,
			map[string]any{"server": "github", "tool": "create_issue", "arguments": args},
		); isError ||
			!strings.Contains(text, `"title":"t"`) {
			t.Errorf("call = %q", text)
		}

		// Arguments beside server and tool, and arguments encoded as a string, are both accepted.
		if text, isError := call(
			t,
			session,
			toolCall,
			map[string]any{"server": "github", "tool": "list_issues", "repo": "r"},
		); isError ||
			!strings.Contains(text, `"repo":"r"`) {
			t.Errorf("flattened call = %q", text)
		}

		if text, isError := call(
			t,
			session,
			toolCall,
			map[string]any{"server": "github", "tool": "list_issues", "arguments": `{"repo":"s"}`},
		); isError ||
			!strings.Contains(text, `"repo":"s"`) {
			t.Errorf("string arguments = %q", text)
		}
	})

	t.Run("errors", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			arguments map[string]any
			code      string
			contains  string
		}{
			{map[string]any{"server": "github", "tool": "create_issue", "arguments": map[string]any{"owner": "o"}}, codeInvalidArguments, `"schema"`},
			{map[string]any{"server": "github", "tool": "create_isue"}, codeUnknownTool, `"suggestions":["create_issue"]`},
			{map[string]any{"server": "github", "tool": "delete_repo"}, codeFilteredTool, "disabled"},
			{map[string]any{"server": "gihub", "tool": "x"}, codeUnknownServer, `"suggestions":["github"]`},
			{map[string]any{"tool": "x"}, codeInvalidArguments, "server and tool are required"},
		}

		for _, test := range cases {
			text, isError := call(t, session, toolCall, test.arguments)

			var payload struct {
				Error toolError `json:"error"`
			}
			if err := json.Unmarshal(
				[]byte(text),
				&payload,
			); err != nil || !isError || payload.Error.Code != test.code ||
				!strings.Contains(text, test.contains) {
				t.Errorf("%v = %s (isError %t)", test.arguments, text, isError)
			}
		}
	})

	t.Run("shaping", func(t *testing.T) {
		t.Parallel()

		result := callResult(t, session, toolCall, map[string]any{"server": "github", "tool": "structured"})
		if result.StructuredContent != nil || len(result.Content) != 1 {
			t.Errorf("structured duplicate kept: %+v", result)
		}

		text, _ := call(t, session, toolCall, map[string]any{"server": "github", "tool": "huge"})
		if !strings.Contains(text, "[truncated: showing 2048 of 5000 bytes") {
			t.Errorf("truncation = %q…", text[max(0, len(text)-200):])
		}
	})
}

func TestDegradedServersAndListChanged(t *testing.T) {
	t.Parallel()

	pool := &fakePool{snapshots: []downstream.Snapshot{
		{Name: "jira", Status: downstream.StatusUnauthorized, Config: &config.Server{Name: "jira"}},
		{
			Name:      "wiki",
			Status:    downstream.StatusUnavailable,
			LastError: "dial tcp: refused",
			RetryAt:   time.Now().Add(30 * time.Second),
			Config:    &config.Server{Name: "wiki"},
		},
	}}

	r := New(Settings{MaxResultBytes: DefMaxResultBytes}, pool, logging.Discard())

	changed := make(chan struct{}, 4)
	session := connectWith(t, r, &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} }})

	description := listTools(t, session)[toolSearch].Description
	if !strings.Contains(description, "- jira (0 tools) [needs login]") || !strings.Contains(description, "- wiki (0 tools) [unavailable]") {
		t.Errorf("description = %s", description)
	}

	text, _ := call(t, session, toolCall, map[string]any{"server": "jira", "tool": "create"})
	if !strings.Contains(text, `"code":"unauthorized"`) || !strings.Contains(text, "mcpcd login jira") {
		t.Errorf("unauthorized = %s", text)
	}

	text, _ = call(t, session, toolCall, map[string]any{"server": "wiki", "tool": "read"})
	if !strings.Contains(text, `"code":"unavailable"`) || !strings.Contains(text, `"retryAfterSeconds":30`) {
		t.Errorf("unavailable = %s", text)
	}

	pool.set([]downstream.Snapshot{{
		Name: "jira", Status: downstream.StatusOK, Config: &config.Server{Name: "jira"},
		Tools: []*mcp.Tool{{Name: "create", InputSchema: map[string]any{"type": "object"}}},
	}})

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list_changed after the catalogue changed")
	}

	if description = listTools(t, session)[toolSearch].Description; !strings.Contains(description, "- jira (1 tool)") {
		t.Errorf("description after change = %s", description)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	t.Parallel()

	content := truncate([]mcp.Content{&mcp.TextContent{Text: strings.Repeat("é", 1000)}}, 1001)

	text, ok := content[0].(*mcp.TextContent)
	if !ok || len(text.Text) != 1000 {
		t.Errorf("first block = %#v, want 1000 bytes of whole two-byte runes", content[0])
	}
}

func TestSignatureTypes(t *testing.T) {
	t.Parallel()

	tool := &mcp.Tool{Name: "t", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state":  map[string]any{"enum": []any{"open", "closed"}},
			"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"id":     map[string]any{"type": []any{"string", "integer"}},
			"any":    map[string]any{},
		},
		"required": []any{"id"},
	}}

	if got, want := signature(tool), `t(id: string|integer, any?: any, labels?: string[], state?: "open"|"closed")`; got != want {
		t.Errorf("signature = %s, want %s", got, want)
	}
}

func (p *fakePool) Snapshots() []downstream.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.snapshots
}

func (p *fakePool) Call(context.Context, *downstream.CallRequest) (*mcp.CallToolResult, error) {
	return p.result, p.err
}

func (*fakePool) CloseSession(string) {}

func (p *fakePool) OnChange(fn func()) {
	p.listeners = append(p.listeners, fn)
}

func connect(t *testing.T, r *Router) *mcp.ClientSession {
	t.Helper()

	return connectWith(t, r, nil)
}

func connectWith(t *testing.T, r *Router, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := r.Server().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, options)

	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})

	return session
}

func listTools(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}

	return tools
}

func call(t *testing.T, session *mcp.ClientSession, tool string, arguments map[string]any) (string, bool) {
	t.Helper()

	result := callResult(t, session, tool, arguments)

	var builder strings.Builder

	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}

	return builder.String(), result.IsError
}

func callResult(t *testing.T, session *mcp.ClientSession, tool string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}

	return result
}

func keys(tools map[string]*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}

	return names
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	return parsed
}

func (p *fakePool) set(snapshots []downstream.Snapshot) {
	p.mu.Lock()
	p.snapshots = snapshots
	p.mu.Unlock()

	for _, listener := range p.listeners {
		listener()
	}
}
