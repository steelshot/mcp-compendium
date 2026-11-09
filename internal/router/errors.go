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
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolError is the single error shape every agent-facing tool returns: a stable code, what went wrong, and the next
// action to take.
type toolError struct {
	Code              string   `json:"code"`
	Message           string   `json:"message"`
	Hint              string   `json:"hint,omitempty"`
	Server            string   `json:"server,omitempty"`
	Tool              string   `json:"tool,omitempty"`
	Suggestions       []string `json:"suggestions,omitempty"`
	RetryAfterSeconds int      `json:"retryAfterSeconds,omitempty"`
	MayHaveRun        bool     `json:"mayHaveRun,omitempty"`
	Errors            []string `json:"errors,omitempty"`
	Schema            any      `json:"schema,omitempty"`
}

// Error codes.
const (
	codeInvalidArguments = "invalid_arguments"
	codeUnknownServer    = "unknown_server"
	codeUnknownTool      = "unknown_tool"
	codeFilteredTool     = "filtered_tool"
	codeUnauthorized     = "unauthorized"
	codeUnavailable      = "unavailable"
	codeTimeout          = "timeout"
	codeDownstreamError  = "downstream_error"

	maxSuggestions = 3
)

func errorResult(failure *toolError) *mcp.CallToolResult {
	data, err := json.Marshal(map[string]*toolError{"error": failure})
	if err != nil {
		data = []byte(`{"error":{"code":"internal","message":"failed to encode error"}}`)
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, IsError: true}
}

// closest returns up to three candidates similar to name: substring matches first, then small edit distances.
func closest(name string, candidates []string) []string {
	type scored struct {
		candidate string
		distance  int
	}

	target := strings.ToLower(name)
	limit := max(2, len(target)/3)
	matches := make([]scored, 0)

	for _, candidate := range candidates {
		lower := strings.ToLower(candidate)

		switch {
		case lower == target:
			matches = append(matches, scored{candidate: candidate})
		case strings.Contains(lower, target) || strings.Contains(target, lower):
			matches = append(matches, scored{candidate: candidate, distance: 1})
		default:
			if distance := levenshtein(target, lower); distance <= limit {
				matches = append(matches, scored{candidate: candidate, distance: distance + 1})
			}
		}
	}

	slices.SortStableFunc(matches, func(a, b scored) int {
		return cmp.Or(cmp.Compare(a.distance, b.distance), cmp.Compare(a.candidate, b.candidate))
	})

	result := make([]string, 0, maxSuggestions)
	for _, match := range matches {
		if len(result) == maxSuggestions {
			break
		}

		result = append(result, match.candidate)
	}

	return result
}

func levenshtein(a, b string) int {
	left, right := []rune(a), []rune(b)
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)

	for column := range previous {
		previous[column] = column
	}

	for row := 1; row <= len(left); row++ {
		current[0] = row

		for column := 1; column <= len(right); column++ {
			cost := 1
			if left[row-1] == right[column-1] {
				cost = 0
			}

			current[column] = min(previous[column]+1, current[column-1]+1, previous[column-1]+cost)
		}

		previous, current = current, previous
	}

	return previous[len(right)]
}
