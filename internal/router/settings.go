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
	"strconv"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings configures the agent-facing tools.
type Settings struct {
	// MaxResultBytes caps the text returned by one call_tool; servers may override it.
	MaxResultBytes int
}

const (
	StrMaxResultBytes = "max-result-bytes"
	EnvMaxResultBytes = app.EnvPrefix + "MAX_RESULT_BYTES"
	// DefMaxResultBytes keeps one result near 8k tokens, a fraction of a typical agent context window.
	DefMaxResultBytes = 32 << 10

	minResultBytes = 1024
)

// Register defines the router flags.
func Register(flags *pflag.FlagSet) {
	flags.Int(StrMaxResultBytes, DefMaxResultBytes, "truncate tool results above this many bytes ("+EnvMaxResultBytes+")")
}

// Defaults maps each router key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrMaxResultBytes: DefMaxResultBytes}
}

// EnvKeys maps each router environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvMaxResultBytes: StrMaxResultBytes}
}

// Load reads and validates the router settings.
func Load(k *koanf.Koanf) (Settings, error) {
	limit, err := kv.Int(k, StrMaxResultBytes, EnvMaxResultBytes)
	if err != nil {
		return Settings{}, err
	}

	if limit < minResultBytes {
		return Settings{}, kv.Invalid(StrMaxResultBytes, EnvMaxResultBytes, strconv.Itoa(limit), "at least 1024")
	}

	return Settings{MaxResultBytes: limit}, nil
}
