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

package downstream

import (
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/kv"
)

// Settings holds the daemon-wide downstream limits; servers may override them in the configuration file.
type Settings struct {
	StartupTimeout   time.Duration
	ExecutionTimeout time.Duration
}

const (
	StrStartupTimeout = "startup-timeout"
	EnvStartupTimeout = app.EnvPrefix + "STARTUP_TIMEOUT"
	DefStartupTimeout = 30 * time.Second

	StrExecutionTimeout = "execution-timeout"
	EnvExecutionTimeout = app.EnvPrefix + "EXECUTION_TIMEOUT"
	DefExecutionTimeout = time.Hour
)

// Register defines the downstream flags.
func Register(flags *pflag.FlagSet) {
	flags.Duration(StrStartupTimeout, DefStartupTimeout, "time allowed to connect to a server and list its tools ("+EnvStartupTimeout+")")
	flags.Duration(StrExecutionTimeout, DefExecutionTimeout, "time allowed for a single tool call ("+EnvExecutionTimeout+")")
}

// Defaults maps each downstream key to its default value.
func Defaults() map[string]any {
	return map[string]any{StrStartupTimeout: DefStartupTimeout, StrExecutionTimeout: DefExecutionTimeout}
}

// EnvKeys maps each downstream environment variable to its key.
func EnvKeys() map[string]string {
	return map[string]string{EnvStartupTimeout: StrStartupTimeout, EnvExecutionTimeout: StrExecutionTimeout}
}

// Load reads and validates the downstream settings.
func Load(k *koanf.Koanf) (Settings, error) {
	startup, err := kv.Duration(k, StrStartupTimeout, EnvStartupTimeout)
	if err != nil {
		return Settings{}, err
	}

	if startup <= 0 {
		return Settings{}, kv.Invalid(StrStartupTimeout, EnvStartupTimeout, startup.String(), "a positive duration")
	}

	execution, err := kv.Duration(k, StrExecutionTimeout, EnvExecutionTimeout)
	if err != nil {
		return Settings{}, err
	}

	if execution <= 0 {
		return Settings{}, kv.Invalid(StrExecutionTimeout, EnvExecutionTimeout, execution.String(), "a positive duration")
	}

	return Settings{StartupTimeout: startup, ExecutionTimeout: execution}, nil
}
