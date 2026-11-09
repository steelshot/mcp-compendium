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
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gobwas/glob"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/router/search"
)

// Exposure is a server's tools split by its configured filters.
type Exposure struct {
	Exposed  []*mcp.Tool
	Filtered []*mcp.Tool
}

// catalogue is an immutable view of every server and its exposed tools, rebuilt whenever a server changes.
type catalogue struct {
	servers   []*serverEntry
	byName    map[string]*serverEntry
	documents []toolRef
	index     *search.Index
	schemas   sync.Map
}

// serverEntry is one server in the catalogue.
type serverEntry struct {
	snapshot downstream.Snapshot
	tools    []*mcp.Tool
	byName   map[string]*mcp.Tool
	filtered map[string]bool
}

// toolRef locates a tool for search results.
type toolRef struct {
	server *serverEntry
	tool   *mcp.Tool
}

// compiledSchema caches a resolved input schema, or the reason it could not be resolved.
type compiledSchema struct {
	resolved *jsonschema.Resolved
	err      error
}

// Partition splits a server's tools into those exposed to agents and those hidden by its configured filters, exactly
// as the agent-facing catalogue does.
func Partition(snapshot *downstream.Snapshot) Exposure {
	entry := newServerEntry(snapshot)
	filtered := make([]*mcp.Tool, 0, len(entry.filtered))

	for _, tool := range snapshot.Tools {
		if entry.filtered[tool.Name] {
			filtered = append(filtered, tool)
		}
	}

	return Exposure{Exposed: entry.tools, Filtered: filtered}
}

// Hints returns the behaviour hints shown to agents for tool, such as read-only.
func Hints(tool *mcp.Tool) []string {
	return hints(tool)
}

func newCatalogue(snapshots []downstream.Snapshot) *catalogue {
	built := &catalogue{byName: make(map[string]*serverEntry, len(snapshots))}
	documents := make([]search.Document, 0)

	for position := range snapshots {
		snapshot := &snapshots[position]
		entry := newServerEntry(snapshot)
		built.servers = append(built.servers, entry)
		built.byName[snapshot.Name] = entry

		for _, tool := range entry.tools {
			built.documents = append(built.documents, toolRef{server: entry, tool: tool})
			documents = append(documents, search.Document{
				Server: snapshot.Name, ServerDescription: snapshot.Description, Tool: tool.Name, Description: tool.Description,
			})
		}
	}

	built.index = search.Build(documents)

	return built
}

// newServerEntry applies the server's include, exclude and read-only filters. Exclude wins over include.
func newServerEntry(snapshot *downstream.Snapshot) *serverEntry {
	entry := &serverEntry{snapshot: *snapshot, byName: make(map[string]*mcp.Tool), filtered: make(map[string]bool)}

	filter := snapshot.Config.Tools
	include := compileGlobs(filter.Include)
	exclude := compileGlobs(filter.Exclude)

	for _, tool := range snapshot.Tools {
		exposed := (len(include) == 0 || matchesAny(include, tool.Name)) && !matchesAny(exclude, tool.Name) &&
			(!filter.ReadOnly || readOnly(tool))
		if !exposed {
			entry.filtered[tool.Name] = true

			continue
		}

		entry.tools = append(entry.tools, tool)
		entry.byName[tool.Name] = tool
	}

	return entry
}

func resolveSchema(raw any) (*jsonschema.Resolved, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}

	var schema jsonschema.Schema
	if err = json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}

	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve schema: %w", err)
	}

	return resolved, nil
}

func compileGlobs(patterns []string) []*glob.Pattern {
	compiled := make([]*glob.Pattern, 0, len(patterns))

	for _, pattern := range patterns {
		// Patterns were validated when the configuration was loaded.
		if matcher, err := glob.Compile(pattern); err == nil {
			compiled = append(compiled, matcher)
		}
	}

	return compiled
}

func matchesAny(patterns []*glob.Pattern, name string) bool {
	for _, pattern := range patterns {
		if pattern.Match(name) {
			return true
		}
	}

	return false
}

func readOnly(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.ReadOnlyHint
}

func idempotent(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.IdempotentHint
}

func destructive(tool *mcp.Tool) bool {
	annotations := tool.Annotations

	return annotations != nil && !annotations.ReadOnlyHint && annotations.DestructiveHint != nil && *annotations.DestructiveHint
}

// schema returns the resolved input schema of a tool, caching the result for the catalogue's lifetime.
func (c *catalogue) schema(server string, tool *mcp.Tool) (*jsonschema.Resolved, error) {
	key := server + "\x00" + tool.Name
	if cached, ok := c.schemas.Load(key); ok {
		if compiled, typed := cached.(compiledSchema); typed {
			return compiled.resolved, compiled.err
		}
	}

	resolved, err := resolveSchema(tool.InputSchema)
	c.schemas.Store(key, compiledSchema{resolved: resolved, err: err})

	return resolved, err
}
