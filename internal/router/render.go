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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/downstream"
)

const (
	maxSentence   = 160
	maxEnumValues = 4
)

// signature renders a tool's arguments compactly, required arguments first: name(owner: string, labels?: string[]).
func signature(tool *mcp.Tool) string {
	schema, _ := tool.InputSchema.(map[string]any)
	properties, _ := schema["properties"].(map[string]any)

	required := make([]string, 0)
	if list, ok := schema["required"].([]any); ok {
		for _, item := range list {
			if name, isString := item.(string); isString {
				if _, exists := properties[name]; exists {
					required = append(required, name)
				}
			}
		}
	}

	optional := make([]string, 0, len(properties))
	for name := range properties {
		if !slices.Contains(required, name) {
			optional = append(optional, name)
		}
	}

	slices.Sort(optional)

	args := make([]string, 0, len(properties))
	for _, name := range required {
		args = append(args, name+": "+typeLabel(properties[name]))
	}

	for _, name := range optional {
		args = append(args, name+"?: "+typeLabel(properties[name]))
	}

	return tool.Name + "(" + strings.Join(args, ", ") + ")"
}

// typeLabel names a property's JSON Schema type the way a TypeScript-literate model reads it.
func typeLabel(raw any) string {
	property, ok := raw.(map[string]any)
	if !ok {
		return "any"
	}

	if values, isEnum := property["enum"].([]any); isEnum && len(values) > 0 && len(values) <= maxEnumValues {
		labels := make([]string, 0, len(values))
		for _, value := range values {
			labels = append(labels, fmt.Sprintf("%q", fmt.Sprint(value)))
		}

		return strings.Join(labels, "|")
	}

	switch kind := property["type"].(type) {
	case string:
		if kind == typeArray {
			return typeLabel(property["items"]) + "[]"
		}

		return kind
	case []any:
		labels := make([]string, 0, len(kind))
		for _, item := range kind {
			if label, isString := item.(string); isString {
				labels = append(labels, label)
			}
		}

		return strings.Join(labels, "|")
	}

	for _, key := range []string{"anyOf", "oneOf"} {
		if variants, isList := property[key].([]any); isList {
			labels := make([]string, 0, len(variants))
			for _, variant := range variants {
				labels = append(labels, typeLabel(variant))
			}

			return strings.Join(labels, "|")
		}
	}

	return "any"
}

// markers flags behaviour an agent should weigh before calling. Only explicit annotations are shown: the spec's
// defaults (destructive unless stated otherwise) would mark nearly every unannotated tool.
func markers(tool *mcp.Tool) string {
	switch {
	case readOnly(tool):
		return " [read-only]"
	case destructive(tool):
		return " [destructive]"
	case idempotent(tool):
		return " [idempotent]"
	default:
		return ""
	}
}

// toolLine renders one search or list result.
func toolLine(server string, tool *mcp.Tool) string {
	line := server + " " + signature(tool) + markers(tool)
	if sentence := firstSentence(tool.Description); sentence != "" {
		line += " - " + sentence
	}

	return line
}

// serverLine renders one server for the catalogue and fallbacks.
func serverLine(entry *serverEntry) string {
	count := len(entry.tools)

	noun := argTools
	if count == 1 {
		noun = argTool
	}

	line := fmt.Sprintf("- %s (%d %s)", entry.snapshot.Name, count, noun)
	if entry.snapshot.Description != "" {
		line += " - " + entry.snapshot.Description
	}

	if status := statusNote(entry.snapshot.Status); status != "" {
		line += " [" + status + "]"
	}

	return line
}

func statusNote(status downstream.Status) string {
	switch status {
	case downstream.StatusConnecting:
		return "connecting"
	case downstream.StatusUnauthorized:
		return "needs login"
	case downstream.StatusUnavailable:
		return "unavailable"
	case downstream.StatusOK:
		return ""
	default:
		return string(status)
	}
}

func firstSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if index := strings.Index(text, ". "); index >= 0 {
		text = text[:index+1]
	}

	if len(text) > maxSentence {
		cut := maxSentence
		for cut > 0 && !isBoundary(text, cut) {
			cut--
		}

		text = strings.TrimSpace(text[:cut]) + "…"
	}

	return text
}

func isBoundary(text string, index int) bool {
	return index >= len(text) || text[index] == ' '
}

func seconds(duration time.Duration) string {
	return strconv.Itoa(max(1, int(duration.Round(time.Second).Seconds())))
}
