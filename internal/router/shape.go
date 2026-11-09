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
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shape reduces a downstream result to what an agent needs, without changing its meaning:
//   - structuredContent that merely duplicates a text block is dropped, and structuredContent with no text
//     representation becomes one, because clients show models the content blocks;
//   - text beyond limit bytes is cut at a UTF-8 boundary with a note saying how much was omitted.
func shape(result *mcp.CallToolResult, limit int) *mcp.CallToolResult {
	shaped := &mcp.CallToolResult{Content: result.Content, StructuredContent: result.StructuredContent, IsError: result.IsError}

	if shaped.StructuredContent != nil {
		if structured, err := json.Marshal(shaped.StructuredContent); err == nil {
			if !hasEquivalentText(shaped.Content, structured) {
				shaped.Content = append(shaped.Content, &mcp.TextContent{Text: string(structured)})
			}

			shaped.StructuredContent = nil
		}
	}

	shaped.Content = truncate(shaped.Content, limit)

	return shaped
}

func hasEquivalentText(content []mcp.Content, structured []byte) bool {
	for _, block := range content {
		text, ok := block.(*mcp.TextContent)
		if !ok {
			continue
		}

		var compacted bytes.Buffer
		if json.Compact(&compacted, []byte(text.Text)) == nil && bytes.Equal(compacted.Bytes(), structured) {
			return true
		}
	}

	return false
}

// truncate keeps whole blocks while they fit, cuts the first text block that does not, and drops the rest.
func truncate(content []mcp.Content, limit int) []mcp.Content {
	total := 0
	for _, block := range content {
		total += blockSize(block)
	}

	if total <= limit {
		return content
	}

	kept := make([]mcp.Content, 0, len(content)+1)
	used := 0

	for _, block := range content {
		size := blockSize(block)
		if used+size <= limit {
			kept = append(kept, block)
			used += size

			continue
		}

		if text, ok := block.(*mcp.TextContent); ok && limit-used > 0 {
			cut := text.Text[:limit-used]
			for !utf8.ValidString(cut) && cut != "" {
				cut = cut[:len(cut)-1]
			}

			kept = append(kept, &mcp.TextContent{Text: cut})
			used += len(cut)
		}

		break
	}

	note := fmt.Sprintf(
		"[truncated: showing %d of %d bytes. Narrow the request (filters, pagination, smaller ranges or fewer fields) to see the rest.]",
		used, total,
	)

	return append(kept, &mcp.TextContent{Text: note})
}

func blockSize(block mcp.Content) int {
	switch typed := block.(type) {
	case *mcp.TextContent:
		return len(typed.Text)
	case *mcp.ImageContent:
		return len(typed.Data)
	case *mcp.AudioContent:
		return len(typed.Data)
	case *mcp.EmbeddedResource:
		if typed.Resource != nil {
			return len(typed.Resource.Text) + len(typed.Resource.Blob)
		}
	}

	return 0
}
