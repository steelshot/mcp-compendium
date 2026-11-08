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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
)

// searchInput is the search_tools argument object.
type searchInput struct {
	Query   string   `json:"query"`
	Servers []string `json:"servers"`
	Limit   int      `json:"limit"`
}

// toolKey names one tool in describe_tools.
type toolKey struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

// describeInput is the describe_tools argument object.
type describeInput struct {
	Tools  []toolKey `json:"tools"`
	Server string    `json:"server"`
}

// callInput is the decoded call_tool argument object.
type callInput struct {
	server    string
	tool      string
	arguments map[string]any
}

// describedTool is one describe_tools result.
type describedTool struct {
	Server      string   `json:"server"`
	Tool        string   `json:"tool"`
	Description string   `json:"description,omitempty"`
	Hints       []string `json:"hints,omitempty"`
	InputSchema any      `json:"inputSchema"`
}

const (
	defaultLimit = 8
	maxLimit     = 25
	maxDescribe  = 25
)

func listServer(built *catalogue, name string) *mcp.CallToolResult {
	entry, ok := built.byName[name]
	if !ok {
		return errorResult(unknownServer(built, name))
	}

	if len(entry.tools) == 0 {
		if failure := statusFailure(entry); failure != nil {
			return errorResult(failure)
		}
	}

	lines := make([]string, 0, len(entry.tools)+1)
	lines = append(lines, strings.TrimPrefix(serverLine(entry), "- "))

	for _, tool := range entry.tools {
		line := signature(tool) + markers(tool)
		if sentence := firstSentence(tool.Description); sentence != "" {
			line += " - " + sentence
		}

		lines = append(lines, line)
	}

	return textResult(strings.Join(lines, "\n"))
}

// lookup resolves server and tool, explaining precisely why a tool cannot be used.
func lookup(built *catalogue, server, tool string) (*serverEntry, *mcp.Tool, *toolError) {
	entry, ok := built.byName[server]
	if !ok {
		return nil, nil, unknownServer(built, server)
	}

	if definition, exists := entry.byName[tool]; exists {
		return entry, definition, nil
	}

	if entry.filtered[tool] {
		return nil, nil, &toolError{
			Code: codeFilteredTool, Server: server, Tool: tool,
			Message: fmt.Sprintf("%s/%s is disabled by the daemon's configuration", server, tool),
			Hint:    "Use a different tool; this one cannot be enabled from the agent.",
		}
	}

	if failure := statusFailure(entry); failure != nil {
		return nil, nil, failure
	}

	return nil, nil, &toolError{
		Code: codeUnknownTool, Server: server, Tool: tool,
		Message:     fmt.Sprintf("%s has no tool %q", server, tool),
		Suggestions: closest(tool, slices.Collect(maps.Keys(entry.byName))),
		Hint:        "Use search_tools, or describe_tools with this server to list its tools.",
	}
}

// statusFailure explains why a server without a live connection has no tools.
func statusFailure(entry *serverEntry) *toolError {
	snapshot := entry.snapshot

	switch snapshot.Status {
	case downstream.StatusUnauthorized:
		return &toolError{
			Code: codeUnauthorized, Server: snapshot.Name, Message: snapshot.Name + " requires authorization",
			Hint: loginHint(snapshot.Name),
		}
	case downstream.StatusUnavailable:
		failure := &toolError{Code: codeUnavailable, Server: snapshot.Name, Message: snapshot.Name + " is unavailable: " + snapshot.LastError}
		if wait := time.Until(snapshot.RetryAt); wait > 0 {
			failure.RetryAfterSeconds = max(1, int(wait.Round(time.Second).Seconds()))
			failure.Hint = "Retry after " + seconds(wait) + "s, or use another server."
		} else {
			failure.Hint = "Retry the call; the daemon reconnects on demand."
		}

		return failure
	case downstream.StatusConnecting:
		return &toolError{
			Code:              codeUnavailable,
			Server:            snapshot.Name,
			Message:           snapshot.Name + " is still connecting",
			RetryAfterSeconds: 2,
			Hint:              "Retry in a few seconds.",
		}
	case downstream.StatusOK:
		return nil
	default:
		return nil
	}
}

func callFailure(server, tool string, err error) *toolError {
	var classified *downstream.Error
	if !errors.As(err, &classified) {
		return &toolError{Code: codeDownstreamError, Server: server, Tool: tool, Message: err.Error()}
	}

	failure := &toolError{Server: server, Tool: tool, Message: classified.Err.Error(), MayHaveRun: classified.MayHaveRun}

	switch classified.Kind {
	case downstream.KindUnauthorized:
		failure.Code, failure.Hint = codeUnauthorized, loginHint(server)
	case downstream.KindTimeout:
		failure.Code, failure.Hint = codeTimeout, "The tool may still have had an effect; check before retrying."
	case downstream.KindUnknownServer:
		failure.Code = codeUnknownServer
	case downstream.KindUnavailable:
		failure.Code = codeUnavailable
		if classified.RetryAfter > 0 {
			failure.RetryAfterSeconds = max(1, int(classified.RetryAfter.Round(time.Second).Seconds()))
		}

		failure.Hint = "Retry the call."
		if classified.MayHaveRun {
			failure.Hint = "The connection failed after the call was sent, so it may have run; check its effect before retrying."
		}
	}

	return failure
}

func unknownServer(built *catalogue, name string) *toolError {
	names := make([]string, 0, len(built.servers))
	for _, entry := range built.servers {
		names = append(names, entry.snapshot.Name)
	}

	return &toolError{
		Code: codeUnknownServer, Server: name, Message: fmt.Sprintf("no server named %q", name),
		Suggestions: closest(name, names), Hint: "Servers: " + strings.Join(names, ", "),
	}
}

func degradedServers(built *catalogue, only []string) []string {
	degraded := make([]string, 0)

	for _, entry := range built.servers {
		if len(only) > 0 && !slices.Contains(only, entry.snapshot.Name) {
			continue
		}

		if note := statusNote(entry.snapshot.Status); note != "" {
			degraded = append(degraded, entry.snapshot.Name+" ("+note+")")
		}
	}

	return degraded
}

func loginHint(server string) string {
	return fmt.Sprintf("Ask the user to run `%s login %s`, which opens a browser, then call again. Do not run it yourself.", app.Binary, server)
}

func hints(tool *mcp.Tool) []string {
	if marker := strings.TrimSpace(markers(tool)); marker != "" {
		return []string{strings.Trim(marker, "[]")}
	}

	return nil
}

// forwardProgress relays downstream progress to the calling client when it asked for progress.
func forwardProgress(req *mcp.CallToolRequest) downstream.ProgressFunc {
	token := req.Params.GetProgressToken()
	if token == nil || req.Session == nil {
		return nil
	}

	return func(ctx context.Context, params *mcp.ProgressNotificationParams) {
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Progress: params.Progress, Total: params.Total, Message: params.Message,
		})
	}
}

// decodeArguments decodes a tool's argument object, rejecting malformed JSON.
func decodeArguments(raw json.RawMessage, target any) *toolError {
	if len(raw) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, target); err != nil {
		return &toolError{Code: codeInvalidArguments, Message: "malformed arguments: " + err.Error()}
	}

	return nil
}

// decodeCall reads call_tool arguments. It tolerates two common model mistakes rather than spending a round trip on
// them: tool arguments sent as a JSON string, and tool arguments placed beside server and tool instead of under
// arguments.
func decodeCall(raw json.RawMessage) (callInput, *toolError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return callInput{}, &toolError{Code: codeInvalidArguments, Message: "malformed arguments: " + err.Error()}
	}

	var server, tool string

	_ = json.Unmarshal(fields[argServer], &server)
	_ = json.Unmarshal(fields[argTool], &tool)

	if server == "" || tool == "" {
		return callInput{}, &toolError{
			Code: codeInvalidArguments, Message: "server and tool are required",
			Hint: `Call with {"server": "...", "tool": "...", "arguments": {...}}; find tools with search_tools.`,
		}
	}

	arguments := map[string]any{}

	rawArguments, present := fields[argArguments]
	switch {
	case present:
		var encoded string
		if json.Unmarshal(rawArguments, &encoded) == nil {
			rawArguments = json.RawMessage(encoded)
		}

		if string(rawArguments) != "null" && json.Unmarshal(rawArguments, &arguments) != nil {
			return callInput{}, &toolError{Code: codeInvalidArguments, Server: server, Tool: tool, Message: "arguments must be a JSON object"}
		}
	default:
		for name, value := range fields {
			if name == argServer || name == argTool {
				continue
			}

			var decoded any
			if json.Unmarshal(value, &decoded) == nil {
				arguments[name] = decoded
			}
		}
	}

	return callInput{server: server, tool: tool, arguments: arguments}, nil
}

func trimValidation(message string) string {
	// jsonschema-go prefixes every message with the chain of schema locations; the last segment is the useful part.
	if index := strings.LastIndex(message, ": "); index >= 0 && strings.Contains(message, "validating") {
		head := message[:index]
		if location := strings.LastIndex(head, "validating "); location >= 0 {
			return strings.TrimPrefix(head[location:], "validating ") + ": " + message[index+2:]
		}
	}

	return message
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func jsonResult(value any) *mcp.CallToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return errorResult(&toolError{Code: "internal", Message: "failed to encode result"})
	}

	return textResult(string(data))
}

func (r *Router) handleSearch(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input searchInput
	if failure := decodeArguments(req.Params.Arguments, &input); failure != nil {
		return errorResult(failure), nil
	}

	if strings.TrimSpace(input.Query) == "" {
		return errorResult(
			&toolError{Code: codeInvalidArguments, Message: "query is required", Hint: `Describe the task, for example "list open pull requests".`},
		), nil
	}

	built := r.catalogue()
	for _, server := range input.Servers {
		if _, ok := built.byName[server]; !ok {
			return errorResult(unknownServer(built, server)), nil
		}
	}

	limit := input.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	limit = min(limit, maxLimit)
	hits := built.index.Search(input.Query, input.Servers, limit)

	lines := make([]string, 0, len(hits)+1)
	for _, hit := range hits {
		ref := built.documents[hit.Index]
		lines = append(lines, toolLine(ref.server.snapshot.Name, ref.tool))
	}

	if len(hits) == 0 {
		lines = append(lines, fmt.Sprintf("No tools matched %q. Try other words, or describe_tools with a server to list its tools. Servers:", input.Query))
		for _, entry := range built.servers {
			lines = append(lines, serverLine(entry))
		}

		return textResult(strings.Join(lines, "\n")), nil
	}

	if degraded := degradedServers(built, input.Servers); len(degraded) > 0 {
		lines = append(lines, "Not searched: "+strings.Join(degraded, ", "))
	}

	return textResult(strings.Join(lines, "\n")), nil
}

func (r *Router) handleDescribe(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input describeInput
	if failure := decodeArguments(req.Params.Arguments, &input); failure != nil {
		return errorResult(failure), nil
	}

	built := r.catalogue()

	if len(input.Tools) == 0 {
		if input.Server == "" {
			return errorResult(&toolError{
				Code: codeInvalidArguments, Message: "pass tools or server",
				Hint: `Use {"tools": [{"server": "...", "tool": "..."}]} for schemas or {"server": "..."} to list a server's tools.`,
			}), nil
		}

		return listServer(built, input.Server), nil
	}

	if len(input.Tools) > maxDescribe {
		return errorResult(&toolError{Code: codeInvalidArguments, Message: fmt.Sprintf("at most %d tools per call", maxDescribe)}), nil
	}

	found := make([]describedTool, 0, len(input.Tools))
	failures := make([]*toolError, 0)

	for _, key := range input.Tools {
		entry, tool, failure := lookup(built, key.Server, key.Tool)
		if failure != nil {
			failures = append(failures, failure)

			continue
		}

		found = append(found, describedTool{
			Server: entry.snapshot.Name, Tool: tool.Name, Description: tool.Description, Hints: hints(tool), InputSchema: tool.InputSchema,
		})
	}

	if len(found) == 0 {
		if len(failures) == 1 {
			return errorResult(failures[0]), nil
		}

		messages := make([]string, 0, len(failures))
		for _, failure := range failures {
			messages = append(messages, failure.Message)
		}

		return errorResult(
			&toolError{Code: codeUnknownTool, Message: "none of the requested tools exist", Errors: messages, Hint: "Use search_tools to find tools."},
		), nil
	}

	payload := map[string]any{argTools: found}
	if len(failures) > 0 {
		payload["errors"] = failures
	}

	return jsonResult(payload), nil
}

func (r *Router) handleCall(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	input, failure := decodeCall(req.Params.Arguments)
	if failure != nil {
		return errorResult(failure), nil
	}

	server, tool, arguments := input.server, input.tool, input.arguments
	resumed := len(req.Params.InputResponses) > 0

	if resumed {
		if result := r.resumeLogin(ctx, req, server); result != nil {
			return result, nil
		}
	}

	built := r.catalogue()

	entry, definition, failure := lookup(built, server, tool)
	if failure != nil {
		if failure.Code == codeUnauthorized && !resumed {
			return r.authorize(ctx, req, server, failure), nil
		}

		return errorResult(failure), nil
	}

	if failure = r.validate(built, entry, definition, arguments); failure != nil {
		return errorResult(failure), nil
	}

	if entry.snapshot.Config.Isolation == config.IsolationSession {
		r.watchSession(req.Session)
	}

	start := time.Now()
	result, err := r.pool.Call(ctx, &downstream.CallRequest{
		Server:    server,
		Tool:      tool,
		Arguments: arguments,
		Retryable: readOnly(definition) || idempotent(definition),
		Progress:  forwardProgress(req),
		Session:   sessionID(req),
	})

	logger := r.logger.With(slog.String("server", server), slog.String("tool", tool), slog.Duration("elapsed", time.Since(start)))

	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("call %s/%s: %w", server, tool, ctx.Err())
		}

		logger.WarnContext(ctx, "call failed", slog.String("error", err.Error()))

		failure = callFailure(server, tool, err)
		if failure.Code == codeUnauthorized && !resumed {
			return r.authorize(ctx, req, server, failure), nil
		}

		return errorResult(failure), nil
	}

	logger.InfoContext(ctx, "call", slog.Bool("isError", result.IsError))

	limit := r.settings.MaxResultBytes
	if override := entry.snapshot.Config.Limits.MaxResultBytes; override > 0 {
		limit = override
	}

	return shape(result, limit), nil
}

// validate checks arguments against the tool's input schema. Schemas the validator cannot compile, for example older
// drafts using unsupported keywords, are skipped and left to the downstream server.
func (r *Router) validate(built *catalogue, entry *serverEntry, tool *mcp.Tool, arguments map[string]any) *toolError {
	resolved, err := built.schema(entry.snapshot.Name, tool)
	if err != nil {
		r.logger.Debug(
			"input schema not validated",
			slog.String("server", entry.snapshot.Name),
			slog.String("tool", tool.Name),
			slog.String("error", err.Error()),
		)

		return nil
	}

	if err = resolved.Validate(arguments); err != nil {
		return &toolError{
			Code: codeInvalidArguments, Server: entry.snapshot.Name, Tool: tool.Name,
			Message: "arguments do not match the tool's input schema: " + trimValidation(err.Error()),
			Hint:    "Fix the arguments to match schema and call again.",
			Schema:  tool.InputSchema,
		}
	}

	return nil
}
