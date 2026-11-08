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
	"path/filepath"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings locates the configuration.
type Settings struct {
	// Path is a configuration file, or a directory whose configuration files are merged in lexical order.
	Path string
}

const (
	StrConfig = "config"
	EnvConfig = app.EnvPrefix + "CONFIG"
	DefConfig = ""
)

// Register defines the configuration flag.
func Register(flags *pflag.FlagSet) {
	flags.StringP(StrConfig, "c", DefConfig, "configuration file or directory, default <user config dir>/"+app.Name+" ("+EnvConfig+")")
}

// Defaults maps each configuration key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrConfig: DefConfig}
}

// EnvKeys maps each configuration environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvConfig: StrConfig}
}

// Load reads the configuration location, defaulting to the per-user configuration directory.
func Load(k *koanf.Koanf) (Settings, error) {
	path := kv.String(k, StrConfig)
	if path != "" {
		return Settings{Path: path}, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return Settings{}, fmt.Errorf("no --%s / %s set and no user configuration directory: %w", StrConfig, EnvConfig, err)
	}

	return Settings{Path: filepath.Join(dir, app.Name)}, nil
}
