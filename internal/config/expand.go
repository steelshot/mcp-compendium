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
	"fmt"
	"os"
	"slices"
	"strings"
)

// lookupFunc resolves environment variables; it matches os.LookupEnv so tests can inject an environment.
type lookupFunc func(name string) (string, bool)

// readFileFunc reads ${file:...} placeholders; it matches os.ReadFile so tests can inject files.
type readFileFunc func(path string) ([]byte, error)

// expander resolves OpenTelemetry Collector-style placeholders in string values:
// ${env:NAME}, ${env:NAME:-default} (default used when NAME is unset or empty), ${file:/path} (one trailing newline
// trimmed, for mounted secrets), and $${ for a literal ${.
type expander struct {
	lookup   lookupFunc
	readFile readFileFunc
}

const maxSecretFileBytes = 64 << 10

func newExpander(lookup lookupFunc, readFile readFileFunc) expander {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	if readFile == nil {
		readFile = os.ReadFile
	}

	return expander{lookup: lookup, readFile: readFile}
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}

	for index, char := range name {
		switch {
		case char == '_', char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9' && index > 0:
		default:
			return false
		}
	}

	return true
}

// expandTree replaces placeholders in every string value of tree in place, reporting failures with their paths.
func (e expander) expandTree(value any, path string, report problems) any {
	switch typed := value.(type) {
	case string:
		expanded, err := e.expandString(typed)
		if err != nil {
			report.add(path, "%v", err)

			return typed
		}

		return expanded
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		for _, key := range keys {
			typed[key] = e.expandTree(typed[key], joinKey(path, key), report)
		}

		return typed
	case []any:
		for index, item := range typed {
			typed[index] = e.expandTree(item, joinIndex(path, index), report)
		}

		return typed
	default:
		return value
	}
}

func (e expander) expandString(input string) (string, error) {
	if !strings.Contains(input, "${") {
		return input, nil
	}

	var builder strings.Builder

	rest := input
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			builder.WriteString(rest)

			return builder.String(), nil
		}

		if start > 0 && rest[start-1] == '$' {
			builder.WriteString(rest[:start-1])
			builder.WriteString("${")
			rest = rest[start+2:]

			continue
		}

		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			return "", fmt.Errorf("%w: unterminated placeholder in %q", errPlaceholder, input)
		}

		resolved, err := e.resolve(rest[start+2 : start+end])
		if err != nil {
			return "", err
		}

		builder.WriteString(rest[:start])
		builder.WriteString(resolved)
		rest = rest[start+end+1:]
	}
}

func (e expander) resolve(expression string) (string, error) {
	scheme, argument, found := strings.Cut(expression, ":")
	if !found {
		return "", fmt.Errorf("%w: ${%s} needs a scheme; use ${env:%s} or ${file:/path}", errPlaceholder, expression, expression)
	}

	switch scheme {
	case "env":
		return e.resolveEnv(argument)
	case "file":
		return e.resolveFile(argument)
	default:
		return "", fmt.Errorf("%w: unknown scheme %q in ${%s}; use env or file", errPlaceholder, scheme, expression)
	}
}

func (e expander) resolveEnv(argument string) (string, error) {
	name, fallback, hasDefault := strings.Cut(argument, ":-")
	if !validEnvName(name) {
		return "", fmt.Errorf("%w: invalid environment variable name %q", errPlaceholder, name)
	}

	if value, ok := e.lookup(name); ok && value != "" {
		return value, nil
	}

	if hasDefault {
		return fallback, nil
	}

	return "", fmt.Errorf("%w: environment variable %s is not set and ${env:%s} has no :-default", errPlaceholder, name, name)
}

func (e expander) resolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: ${file:} needs a path", errPlaceholder)
	}

	data, err := e.readFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: read ${file:%s}: %w", errPlaceholder, path, err)
	}

	if len(data) > maxSecretFileBytes {
		return "", fmt.Errorf("%w: ${file:%s} is larger than %d bytes", errPlaceholder, path, maxSecretFileBytes)
	}

	text := strings.TrimSuffix(string(data), "\n")

	return strings.TrimSuffix(text, "\r"), nil
}
