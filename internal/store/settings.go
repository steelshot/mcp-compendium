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

package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings locates the writable state directory.
type Settings struct {
	Dir string
}

const (
	StrStateDir = "state-dir"
	EnvStateDir = app.EnvPrefix + "STATE_DIR"
	DefStateDir = ""
)

// Register defines the state directory flag.
func Register(flags *pflag.FlagSet) {
	flags.String(StrStateDir, DefStateDir, "writable directory for OAuth tokens, default <user state dir>/"+app.Name+" ("+EnvStateDir+")")
}

// Defaults maps each store key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrStateDir: DefStateDir}
}

// EnvKeys maps each store environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvStateDir: StrStateDir}
}

// Load reads the state directory, defaulting to $XDG_STATE_HOME/mcp-compendium, ~/.local/state/mcp-compendium, or the
// per-user cache directory on platforms without an XDG state directory.
func Load(k *koanf.Koanf) (Settings, error) {
	if dir := kv.String(k, StrStateDir); dir != "" {
		return Settings{Dir: dir}, nil
	}

	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return Settings{Dir: filepath.Join(xdg, app.Name)}, nil
	}

	if home, err := os.UserHomeDir(); err == nil && filepath.Separator == '/' {
		return Settings{Dir: filepath.Join(home, ".local", "state", app.Name)}, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return Settings{}, fmt.Errorf("no --%s / %s set and no user directory to default to: %w", StrStateDir, EnvStateDir, err)
	}

	return Settings{Dir: filepath.Join(dir, app.Name, "state")}, nil
}
