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

// Package testmcp provides fake MCP servers for tests: an in-process Streamable HTTP server and a stdio server run by
// re-executing the test binary.
package testmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is a fake tool definition.
type Tool struct {
	Name        string
	Description string
	ReadOnly    bool
	Idempotent  bool
	Destructive bool
	// Properties maps argument names to JSON Schema types; Required lists mandatory arguments.
	Properties map[string]string
	Required   []string
	// Schema, when set, is used verbatim as the input schema instead of Properties and Required.
	Schema  map[string]any
	Handler func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, error)
}

// StdioEnv is the environment variable that makes a test binary act as a stdio server; see RunStdioIfRequested.
const StdioEnv = "MCPCD_TESTMCP_STDIO"

// NewServer returns an MCP server exposing tools.
func NewServer(name, title, instructions string, tools ...Tool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: name, Title: title, Version: "test"}, &mcp.ServerOptions{Instructions: instructions})
	for _, tool := range tools {
		AddTool(server, &tool)
	}

	return server
}

// AddTool registers tool on server, replacing any tool with the same name.
func AddTool(server *mcp.Server, tool *Tool) {
	properties := make(map[string]any, len(tool.Properties))
	for name, kind := range tool.Properties {
		properties[name] = map[string]any{"type": kind, "description": name + " argument"}
	}

	schema := map[string]any{"type": "object", "properties": properties}
	if len(tool.Required) > 0 {
		schema["required"] = tool.Required
	}

	if tool.Schema != nil {
		schema = tool.Schema
	}

	definition := &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema}
	if tool.ReadOnly || tool.Idempotent || tool.Destructive {
		destructive := tool.Destructive
		definition.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: tool.ReadOnly, IdempotentHint: tool.Idempotent, DestructiveHint: &destructive}
	}

	handler := tool.Handler
	if handler == nil {
		handler = Echo
	}

	server.AddTool(definition, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("decode arguments: %w", err)
			}
		}

		return handler(ctx, req, args)
	})
}

// Echo returns the tool name and arguments as text.
func Echo(_ context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode arguments: %w", err)
	}

	return Text(req.Params.Name + " " + string(data)), nil
}

// Text returns a result with a single text block.
func Text(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// Progress sends count progress notifications, pausing briefly between them, before echoing.
func Progress(count int) func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, error) {
		token := req.Params.GetProgressToken()
		for step := 1; step <= count && token != nil; step++ {
			if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token, Progress: float64(step), Total: float64(count), Message: fmt.Sprintf("step %d", step),
			}); err != nil {
				return nil, fmt.Errorf("notify progress: %w", err)
			}

			time.Sleep(10 * time.Millisecond)
		}

		return Echo(ctx, req, args)
	}
}

// Crash exits the stdio server process, simulating a downstream crash.
func Crash(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
	os.Exit(3) //nolint:revive // simulates a crashing downstream process

	return nil, nil //nolint:nilnil // unreachable after os.Exit
}

// HTTPHandler returns the Streamable HTTP handler for server.
func HTTPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}

// ServeHTTP serves server over Streamable HTTP until the test ends and returns its endpoint URL.
func ServeHTTP(t *testing.T, server *mcp.Server) string {
	t.Helper()

	return ServeHTTPWith(t, HTTPHandler(server))
}

// ServeHTTPWith serves handler until the test ends and returns its URL.
func ServeHTTPWith(t *testing.T, handler http.Handler) string {
	t.Helper()

	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	return httpServer.URL
}

// RunStdioIfRequested serves server over stdio when the test binary was re-executed as a stdio server, returning the
// exit code and true; TestMain must then exit with that code instead of running the tests.
func RunStdioIfRequested(server func() *mcp.Server) (int, bool) {
	if os.Getenv(StdioEnv) == "" {
		return 0, false
	}

	if err := server().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "testmcp stdio:", err)

		return 1, true
	}

	return 0, true
}
