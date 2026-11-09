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

package cli

import (
	"fmt"
	"maps"
	"os"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
)

// settingsModule is the koanf-facing half of a package's settings: its defaults and environment variable names.
type settingsModule struct {
	defaults func() map[string]any
	envKeys  func() map[string]string
}

// loadSettings merges defaults, then known environment variables, then flags the user set explicitly. Flag defaults
// never override the environment because posflag skips unchanged flags whose keys already exist.
func loadSettings(flags *pflag.FlagSet, environ func() []string, modules ...settingsModule) (*koanf.Koanf, error) {
	defaults := make(map[string]any)
	envKeys := make(map[string]string)

	for _, module := range modules {
		maps.Copy(defaults, module.defaults())
		maps.Copy(envKeys, module.envKeys())
	}

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}

	if environ == nil {
		environ = os.Environ
	}

	envProvider := env.Provider(".", env.Opt{
		Prefix:      app.EnvPrefix,
		EnvironFunc: environ,
		TransformFunc: func(name, value string) (string, any) {
			return envKeys[name], value
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("load environment: %w", err)
	}

	if err := k.Load(posflag.Provider(flags, ".", k), nil); err != nil {
		return nil, fmt.Errorf("load flags: %w", err)
	}

	return k, nil
}
