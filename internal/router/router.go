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

// Package router serves the agent-facing MCP surface: search_tools, describe_tools and call_tool over every
// downstream server, with a compact catalogue, structured errors and result shaping.
package router

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/downstream"
)

// Pool is the downstream connection pool the router routes to.
type Pool interface {
	Snapshots() []downstream.Snapshot
	Call(ctx context.Context, req *downstream.CallRequest) (*mcp.CallToolResult, error)
	CloseSession(session string)
	OnChange(fn func())
}

// Router owns the agent-facing MCP server.
type Router struct {
	settings Settings
	pool     Pool
	logger   *slog.Logger
	server   *mcp.Server
	current  atomic.Pointer[catalogue]

	mu          sync.Mutex
	description string
	logins      *elicitedLogins
	sessions    map[string]bool
}

const (
	toolSearch   = "search_tools"
	toolDescribe = "describe_tools"
	toolCall     = "call_tool"

	argQuery     = "query"
	argServers   = "servers"
	argLimit     = "limit"
	argTools     = "tools"
	argServer    = "server"
	argTool      = "tool"
	argArguments = "arguments"

	typeObject  = "object"
	typeString  = "string"
	typeInteger = "integer"
	typeArray   = "array"

	instructions = "This server routes to many MCP servers. Find tools with search_tools by describing the task, then " +
		"run them with call_tool. Use describe_tools when a one-line signature is not enough."

	searchIntro = "Find tools across the connected MCP servers by describing what you want to do, for example " +
		`"create a GitHub issue". Returns ranked one-line signatures (server tool(args) [hints] - summary); arguments ` +
		"ending in ? are optional. Call call_tool directly when the signature is enough."
)

// New builds the router and subscribes it to pool changes.
func New(settings Settings, pool Pool, logger *slog.Logger) *Router {
	r := &Router{settings: settings, pool: pool, logger: logger, sessions: make(map[string]bool)}
	r.server = mcp.NewServer(
		&mcp.Implementation{Name: app.Name, Title: "MCP Compendium", Version: app.Version()},
		&mcp.ServerOptions{
			Instructions: instructions,
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		},
	)

	r.server.AddTool(describeTool(), r.handleDescribe)
	r.server.AddTool(callTool(), r.handleCall)
	r.rebuild()
	pool.OnChange(r.rebuild)

	return r
}

// Server returns the MCP server to expose over a transport.
func (r *Router) Server() *mcp.Server {
	return r.server
}

// searchDescription embeds the server catalogue so agents know what exists before their first call.
func searchDescription(built *catalogue) string {
	if len(built.servers) == 0 {
		return searchIntro + "\n\nNo servers are configured."
	}

	lines := make([]string, 0, len(built.servers)+2)
	lines = append(lines, searchIntro, "", "Servers:")

	for _, entry := range built.servers {
		lines = append(lines, serverLine(entry))
	}

	return strings.Join(lines, "\n")
}

func searchTool(description string) *mcp.Tool {
	limit := property(typeInteger, "Maximum results.")
	limit["minimum"], limit["maximum"], limit["default"] = 1, maxLimit, defaultLimit

	return &mcp.Tool{
		Name:        toolSearch,
		Title:       "Search tools",
		Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
		InputSchema: objectSchema(map[string]any{
			argQuery:   property(typeString, "What you want to do, in plain words or keywords."),
			argServers: arrayProperty(property(typeString, ""), "Only search these servers."),
			argLimit:   limit,
		}, argQuery),
	}
}

func describeTool() *mcp.Tool {
	key := objectSchema(map[string]any{argServer: property(typeString, ""), argTool: property(typeString, "")}, argServer, argTool)
	tools := arrayProperty(key, "Tools to describe.")
	tools["maxItems"] = maxDescribe

	return &mcp.Tool{
		Name:  toolDescribe,
		Title: "Describe tools",
		Description: "Get the full description and JSON input schema of specific tools, which may span servers, or pass " +
			"only server to list every tool of that server as one-line signatures.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
		InputSchema: objectSchema(map[string]any{
			argTools:  tools,
			argServer: property(typeString, "List every tool of this server instead."),
		}),
	}
}

func callTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  toolCall,
		Title: "Call a tool",
		Description: "Call a tool on a connected MCP server. Arguments are checked against the tool's schema first; " +
			"a mismatch returns the schema so you can fix the call in one step. Find tools with search_tools.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(true)},
		InputSchema: objectSchema(map[string]any{
			argServer:    property(typeString, "Server name from search_tools."),
			argTool:      property(typeString, "Tool name on that server."),
			argArguments: property(typeObject, "Arguments matching the tool's input schema."),
		}, argServer, argTool),
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": typeObject, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

func property(kind, description string) map[string]any {
	schema := map[string]any{"type": kind}
	if description != "" {
		schema["description"] = description
	}

	return schema
}

func arrayProperty(items map[string]any, description string) map[string]any {
	schema := property(typeArray, description)
	schema["items"] = items

	return schema
}

// rebuild swaps in a fresh catalogue and, when the server list in the search_tools description changed, re-registers
// the tool, which notifies connected clients with notifications/tools/list_changed.
func (r *Router) rebuild() {
	r.mu.Lock()
	defer r.mu.Unlock()

	built := newCatalogue(r.pool.Snapshots())
	r.current.Store(built)

	description := searchDescription(built)
	if description == r.description {
		return
	}

	r.description = description
	r.server.AddTool(searchTool(description), r.handleSearch)
}

// watchSession releases a client session's isolated connections when the session ends.
func (r *Router) watchSession(session *mcp.ServerSession) {
	if session == nil || session.ID() == "" {
		return
	}

	r.mu.Lock()
	seen := r.sessions[session.ID()]
	r.sessions[session.ID()] = true
	r.mu.Unlock()

	if seen {
		return
	}

	go func() {
		_ = session.Wait()

		r.pool.CloseSession(session.ID())

		r.mu.Lock()
		delete(r.sessions, session.ID())
		r.mu.Unlock()
	}()
}

func (r *Router) catalogue() *catalogue {
	return r.current.Load()
}
