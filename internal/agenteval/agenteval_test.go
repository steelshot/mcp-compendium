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

package agenteval

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/router"
	"github.com/steelshot/mcp-compendium/internal/testmcp"
)

// catalogue is the fixture of fake downstream servers.
type catalogue struct {
	Servers map[string]struct {
		Description string        `json:"description"`
		Tools       []fixtureTool `json:"tools"`
	} `json:"servers"`
}

// fixtureTool is one fake downstream tool.
type fixtureTool struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	ReadOnly      bool           `json:"readOnly"`
	Properties    map[string]any `json:"properties"`
	Required      []string       `json:"required"`
	ResponseBytes int            `json:"responseBytes"`
}

// task is one scripted agent task. Mistake, when set, is sent before Arguments to measure error recovery.
type task struct {
	ID        string         `json:"id"`
	Intent    string         `json:"intent"`
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Mistake   map[string]any `json:"mistake"`
}

// metrics is what one task cost. Rank is the target tool's position in the search results, or 0 when it was missing.
type metrics struct {
	Rank   int `json:"rank"`
	Rounds int `json:"rounds"`
	Tokens int `json:"tokens"`
}

// baseline is the committed reference the current results are compared with.
type baseline struct {
	ListTokens       int                `json:"listTokens"`
	DirectListTokens int                `json:"directListTokens"`
	Tasks            map[string]metrics `json:"tasks"`
}

// agent drives the agent-facing tools the way the tool descriptions tell a model to.
type agent struct {
	t       *testing.T
	session *mcp.ClientSession
	rounds  int
	tokens  int
}

const (
	baselinePath = "testdata/baseline.json"
	// Tolerances before a change counts as a regression.
	listTolerance  = 1.05
	tokenTolerance = 1.10
)

//nolint:gochecknoglobals // test flag
var update = flag.Bool("update", false, "write the current results to testdata/baseline.json")

func TestAgentTasks(t *testing.T) {
	t.Parallel()

	fixture := readJSON[catalogue](t, "testdata/catalogue.json")
	tasks := readJSON[[]task](t, "testdata/tasks.json")

	pool := startPool(t, fixture)
	session := connect(t, router.New(router.Settings{MaxResultBytes: router.DefMaxResultBytes}, pool, logging.Discard()))

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	current := baseline{ListTokens: tokensOf(t, listed.Tools), DirectListTokens: directListTokens(t, pool), Tasks: map[string]metrics{}}

	for _, item := range tasks {
		current.Tasks[item.ID] = (&agent{t: t, session: session}).run(&item)
	}

	report(t, tasks, current)

	if *update {
		writeBaseline(t, current)

		return
	}

	compare(t, readJSON[baseline](t, baselinePath), current)
}

// rankOf finds server and tool in search_tools output, whose lines read "server tool(args) [hints] - summary".
func rankOf(text, server, tool string) int {
	rank := 0

	for line := range strings.SplitSeq(text, "\n") {
		name, rest, ok := strings.Cut(line, " ")
		candidate, _, hasArgs := strings.Cut(rest, "(")

		if !ok || !hasArgs {
			continue
		}

		rank++

		if name == server && candidate == tool {
			return rank
		}
	}

	return 0
}

func startPool(t *testing.T, fixture catalogue) *downstream.Manager {
	t.Helper()

	servers := make([]*config.Server, 0, len(fixture.Servers))

	for name, definition := range fixture.Servers {
		fake := testmcp.NewServer(name, "", "")
		for _, tool := range definition.Tools {
			testmcp.AddTool(fake, &testmcp.Tool{
				Name: tool.Name, Description: tool.Description, ReadOnly: tool.ReadOnly,
				Schema:  map[string]any{"type": "object", "properties": tool.Properties, "required": tool.Required},
				Handler: respond(tool.ResponseBytes),
			})
		}

		endpoint, err := url.Parse(testmcp.ServeHTTP(t, fake))
		if err != nil {
			t.Fatal(err)
		}

		servers = append(
			servers,
			&config.Server{Name: name, Transport: config.TransportHTTP, Enabled: true, URL: endpoint, Description: definition.Description},
		)
	}

	slices.SortFunc(servers, func(a, b *config.Server) int { return strings.Compare(a.Name, b.Name) })

	pool := downstream.New(servers, downstream.Settings{StartupTimeout: 10 * time.Second, ExecutionTimeout: 10 * time.Second}, nil, logging.Discard())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		pool.Close()
	})
	pool.Start(ctx)

	if err := pool.WaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}

	return pool
}

// respond echoes the call, padded to size bytes for tools whose real counterparts return large results.
func respond(size int) func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, error) {
		if size == 0 {
			return testmcp.Echo(ctx, req, args)
		}

		var builder strings.Builder

		builder.WriteString(req.Params.Name + "\n")

		for entry := 0; builder.Len() < size; entry++ {
			_, _ = fmt.Fprintf(&builder, `{"name": "entry-%05d.txt", "type": "file"},`+"\n", entry)
		}

		return testmcp.Text(builder.String()), nil
	}
}

// directListTokens is what tools/list would cost if every downstream tool were exposed to the agent directly.
func directListTokens(t *testing.T, pool *downstream.Manager) int {
	t.Helper()

	tools := make([]*mcp.Tool, 0)
	for _, snapshot := range pool.Snapshots() {
		tools = append(tools, snapshot.Tools...)
	}

	return tokensOf(t, tools)
}

func connect(t *testing.T, r *router.Router) *mcp.ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := r.Server().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	session, err := mcp.NewClient(&mcp.Implementation{Name: "agenteval", Version: "test"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})

	return session
}

func report(t *testing.T, tasks []task, current baseline) {
	t.Helper()

	var builder strings.Builder

	table := tabwriter.NewWriter(&builder, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(table, "task\trank\trounds\ttokens\t")

	found, rounds, tokens := 0, 0, 0

	for _, item := range tasks {
		result := current.Tasks[item.ID]
		_, _ = fmt.Fprintf(table, "%s\t%d\t%d\t%d\t\n", item.ID, result.Rank, result.Rounds, result.Tokens)

		if result.Rank > 0 {
			found++
		}

		rounds += result.Rounds
		tokens += result.Tokens
	}

	_ = table.Flush()

	t.Logf("\n%s\nfound by search: %d/%d, rounds: %d, task tokens: %d\ntools/list tokens: %d (direct exposure: %d, %.0f%% saved)",
		builder.String(), found, len(tasks), rounds, tokens, current.ListTokens, current.DirectListTokens,
		100*(1-float64(current.ListTokens)/float64(current.DirectListTokens)))
}

func compare(t *testing.T, reference, current baseline) {
	t.Helper()

	if float64(current.ListTokens) > float64(reference.ListTokens)*listTolerance {
		t.Errorf("tools/list grew from %d to %d tokens", reference.ListTokens, current.ListTokens)
	}

	for id, now := range current.Tasks {
		before, ok := reference.Tasks[id]
		if !ok {
			t.Errorf("%s: no baseline; run go test ./internal/agenteval -update", id)

			continue
		}

		if before.Rank > 0 && (now.Rank == 0 || now.Rank > before.Rank) {
			t.Errorf("%s: search rank fell from %d to %d", id, before.Rank, now.Rank)
		}

		if now.Rounds > before.Rounds {
			t.Errorf("%s: round trips rose from %d to %d", id, before.Rounds, now.Rounds)
		}

		if float64(now.Tokens) > float64(before.Tokens)*tokenTolerance {
			t.Errorf("%s: tokens rose from %d to %d", id, before.Tokens, now.Tokens)
		}
	}
}

func writeBaseline(t *testing.T, current baseline) {
	t.Helper()

	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(filepath.FromSlash(baselinePath), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Logf("wrote %s", baselinePath)
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()

	var value T

	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}

	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	return value
}

// tokensOf estimates the tokens of value's JSON encoding.
func tokensOf(t *testing.T, value any) int {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return estimate(string(data))
}

// estimate approximates a token count as one token per four bytes, the usual rule of thumb for English and JSON with
// current tokenizers. Only relative changes matter here, so the approximation's bias cancels out.
func estimate(text string) int {
	return (len(text) + 3) / 4
}

// run completes one task: search with the intent, list the server's tools when the target was not found, optionally
// make the scripted mistake, then call the tool.
func (a *agent) run(item *task) metrics {
	a.t.Helper()

	text, _ := a.call("search_tools", map[string]any{"query": item.Intent})
	rank := rankOf(text, item.Server, item.Tool)

	if rank == 0 {
		a.call("describe_tools", map[string]any{"server": item.Server})
	}

	if item.Mistake != nil {
		failure, isError := a.call("call_tool", map[string]any{"server": item.Server, "tool": item.Tool, "arguments": item.Mistake})
		if !isError || !strings.Contains(failure, `"code":"invalid_arguments"`) || !strings.Contains(failure, `"schema"`) {
			a.t.Errorf("%s: the mistake was not answered with invalid_arguments and the schema: %s", item.ID, failure)
		}
	}

	text, isError := a.call("call_tool", map[string]any{"server": item.Server, "tool": item.Tool, "arguments": item.Arguments})
	if isError || !strings.HasPrefix(text, item.Tool) {
		a.t.Errorf("%s: call failed: %.300s", item.ID, text)
	}

	return metrics{Rank: rank, Rounds: a.rounds, Tokens: a.tokens}
}

// call invokes one agent-facing tool, counting the round trip and the tokens of the arguments and the response.
func (a *agent) call(tool string, arguments map[string]any) (string, bool) {
	a.t.Helper()

	result, err := a.session.CallTool(a.t.Context(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		a.t.Fatalf("%s: %v", tool, err)
	}

	var builder strings.Builder

	for _, content := range result.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(block.Text)
		}
	}

	a.rounds++
	a.tokens += tokensOf(a.t, arguments) + estimate(builder.String())

	return builder.String(), result.IsError
}
