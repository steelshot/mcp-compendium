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

// Package app holds the application identity shared by every package: names, the environment variable prefix and
// build metadata injected at link time.
package app

import (
	"runtime/debug"
)

// Info describes the running build.
type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
}

const (
	// Name is the project name used for images, the Helm chart and filesystem paths.
	Name = "mcp-compendium"
	// Binary is the executable name.
	Binary = "mcpcd"
	// EnvPrefix prefixes every environment variable the binary reads.
	EnvPrefix = "MCP_COMPENDIUM_"
)

// Set at link time by GoReleaser (.goreleaser.yaml) via -ldflags "-X". Empty values fall back to the
// toolchain's embedded build info.
//
//nolint:gochecknoglobals // link-time injected build metadata
var (
	version = ""
	commit  = ""
	date    = ""
)

// BuildInfo returns the build metadata, falling back to the module and VCS information embedded by the Go toolchain
// when the binary was not built by the release build.
func BuildInfo() Info {
	info := Info{Version: version, Commit: commit, Date: date}

	embedded, ok := debug.ReadBuildInfo()
	if !ok {
		return withDefaults(info)
	}

	info.GoVersion = embedded.GoVersion
	if info.Version == "" && embedded.Main.Version != "" && embedded.Main.Version != "(devel)" {
		info.Version = embedded.Main.Version
	}

	for _, setting := range embedded.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = setting.Value
			}
		}
	}

	return withDefaults(info)
}

// Version returns the version string reported to MCP peers and by the version command.
func Version() string {
	return BuildInfo().Version
}

func withDefaults(info Info) Info {
	if info.Version == "" {
		info.Version = "dev"
	}

	if info.Commit == "" {
		info.Commit = "unknown"
	}

	if info.Date == "" {
		info.Date = "unknown"
	}

	return info
}
