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

package logging

import (
	"log/slog"
	"strings"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings configures the logger.
type Settings struct {
	Level  slog.Level
	Format string
}

const (
	StrLevel = "log-level"
	EnvLevel = app.EnvPrefix + "LOG_LEVEL"
	DefLevel = "info"

	StrFormat = "log-format"
	EnvFormat = app.EnvPrefix + "LOG_FORMAT"
	DefFormat = formatJSON

	formatJSON = "json"
	formatText = "text"
)

// Register defines the logging flags.
func Register(flags *pflag.FlagSet) {
	flags.String(StrLevel, DefLevel, "log level: debug, info, warn or error ("+EnvLevel+")")
	flags.String(StrFormat, DefFormat, "log format: json or text ("+EnvFormat+")")
}

// Defaults maps each logging key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrLevel: DefLevel, StrFormat: DefFormat}
}

// EnvKeys maps each logging environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvLevel: StrLevel, EnvFormat: StrFormat}
}

// Load reads and validates the logging settings.
func Load(k *koanf.Koanf) (Settings, error) {
	levelName, err := kv.OneOf(k, StrLevel, EnvLevel, "debug", "info", "warn", "error")
	if err != nil {
		return Settings{}, err
	}

	var level slog.Level
	if err = level.UnmarshalText([]byte(strings.ToUpper(levelName))); err != nil {
		return Settings{}, kv.Invalid(StrLevel, EnvLevel, levelName, "debug, info, warn or error")
	}

	format, err := kv.OneOf(k, StrFormat, EnvFormat, formatJSON, formatText)
	if err != nil {
		return Settings{}, err
	}

	return Settings{Level: level, Format: format}, nil
}
