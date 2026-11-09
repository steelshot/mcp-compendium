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
	"testing"
)

// FuzzDecodeCall checks that call_tool input either fails with an invalid-arguments error or yields a server, a tool
// and an argument object.
func FuzzDecodeCall(f *testing.F) {
	for _, seed := range []string{
		`{"server": "s", "tool": "t", "arguments": {"a": 1}}`,
		`{"server": "s", "tool": "t", "arguments": "{\"a\": 1}"}`,
		`{"server": "s", "tool": "t", "a": 1, "b": [true]}`,
		`{"server": "s", "tool": "t", "arguments": null}`,
		`{"server": "s", "tool": "t", "arguments": [1]}`,
		`{"server": 1}`, `[]`, `null`, ``,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		input, failure := decodeCall(raw)
		if failure != nil {
			if failure.Code != codeInvalidArguments || failure.Message == "" {
				t.Fatalf("decodeCall(%q) failed with %+v", raw, failure)
			}

			return
		}

		if input.server == "" || input.tool == "" || input.arguments == nil {
			t.Fatalf("decodeCall(%q) = %+v; want a server, a tool and arguments", raw, input)
		}

		if _, err := json.Marshal(input.arguments); err != nil {
			t.Fatalf("decodeCall(%q) arguments do not re-encode: %v", raw, err)
		}
	})
}
