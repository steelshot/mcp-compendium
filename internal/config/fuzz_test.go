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

package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// FuzzDecode checks that every decoder either fails or yields only the documented tree types.
func FuzzDecode(f *testing.F) {
	seeds := map[string]string{
		".json":  `{"servers": {"a": {"command": "x", "args": ["-v", 1, 2.5, true, null]}}}`,
		".jsonc": "{\n  // comment\n  \"a\": [1, 2,],\n}",
		".yaml":  fullYAML,
		".toml":  "[servers.a]\ncommand = \"x\"\nargs = [\"-v\"]\n[servers.a.env]\nA = \"${env:A:-b}\"\n",
	}
	for index, extension := range fuzzExtensions() {
		f.Add(uint8(index), []byte(seeds[extension]))
	}

	f.Add(uint8(2), []byte("a: &x [1]\nb: *x\nc: .nan\nd: 18446744073709551615\n"))

	f.Fuzz(func(t *testing.T, extension uint8, data []byte) {
		tree, err := decode(fuzzName(extension), data)
		if err != nil {
			return
		}

		checkTree(t, "", tree)
	})
}

// FuzzLoad runs decoded input through placeholder expansion, parsing and merging, which must report problems rather
// than panic.
func FuzzLoad(f *testing.F) {
	f.Add(uint8(2), []byte(fullYAML))
	f.Add(uint8(0), []byte(`{"servers": {"a": {"type": "http", "url": "${env:URL}", "headers": {"X": "${file:/k}"}}}}`))
	f.Add(uint8(0), []byte(`{"auth": {"allowAnonymous": true, "providers": [{"name": "p", "type": "basic", "users": []}]}}`))

	f.Fuzz(func(t *testing.T, extension uint8, data []byte) {
		tree, err := decode(fuzzName(extension), data)
		if err != nil {
			return
		}

		var list []Problem

		report := newProblems("fuzz", &list)
		fuzzExpander().expandTree(tree, "", report)
		merge([]fragment{parseFragment("fuzz", tree, report)}, &list)

		for _, problem := range list {
			if problem.Message == "" {
				t.Fatalf("problem without a message: %+v", problem)
			}
		}
	})
}

// FuzzExpandString checks placeholder expansion: text without "${" is returned unchanged, and every failure wraps
// errPlaceholder.
func FuzzExpandString(f *testing.F) {
	for _, seed := range []string{
		"plain", "${env:HOME}", "${env:UNSET:-fallback}", "$${env:HOME}", "${file:/secret}", "${env:}", "${nope}",
		"a${env:HOME}b${env:UNSET:-}c", "${", "}${", "$$${env:HOME}",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		expanded, err := fuzzExpander().expandString(input)
		if !strings.Contains(input, "${") {
			if err != nil || expanded != input {
				t.Fatalf("expandString(%q) = %q, %v; want the input unchanged", input, expanded, err)
			}

			return
		}

		if err != nil && !errors.Is(err, errPlaceholder) {
			t.Fatalf("expandString(%q) error %v does not wrap errPlaceholder", input, err)
		}
	})
}

func fuzzExtensions() []string {
	return []string{".json", ".jsonc", ".yaml", ".toml"}
}

// fuzzName maps a fuzzed byte onto a file name with one of the supported extensions.
func fuzzName(extension uint8) string {
	extensions := fuzzExtensions()

	return "fuzz" + extensions[int(extension)%len(extensions)]
}

func fuzzExpander() expander {
	environment := map[string]string{"HOME": "/home/x", "URL": "https://example.test/mcp", "EMPTY": ""}

	return newExpander(
		func(name string) (string, bool) {
			value, ok := environment[name]

			return value, ok
		},
		func(path string) ([]byte, error) {
			if path == "/secret" || path == "/k" {
				return []byte("value\n"), nil
			}

			return nil, fs.ErrNotExist
		},
	)
}

func checkTree(t *testing.T, path string, value any) {
	t.Helper()

	switch typed := value.(type) {
	case nil, string, bool, int64, float64:
	case map[string]any:
		for key, item := range typed {
			checkTree(t, joinKey(path, key), item)
		}
	case []any:
		for index, item := range typed {
			checkTree(t, joinIndex(path, index), item)
		}
	default:
		t.Fatalf("%s: unexpected %T in decoded tree", path, value)
	}
}
