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

// Package cli implements the mcpcd command tree. Each command assembles its settings from the packages it uses and
// injects the resulting typed values into constructors; koanf does not leak past this package and settings.go files.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/app"
)

// streams carries the process I/O and environment so commands stay testable.
type streams struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	environ func() []string
}

// Execute runs the command tree with args and returns the process exit code.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	//nolint:contextcheck // cobra hands ctx to every command through cmd.Context() via ExecuteContext below
	root := newRootCommand(streams{stdin: stdin, stdout: stdout, stderr: stderr, environ: os.Environ})
	root.SetArgs(args)

	if err := root.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "Error:", err)

		return 1
	}

	return 0
}

func newRootCommand(std streams) *cobra.Command {
	root := &cobra.Command{
		Use:           app.Binary,
		Short:         "Serve many MCP servers to AI agents as one compact, searchable endpoint",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       app.Version(),
	}
	root.SetOut(std.stdout)
	root.SetErr(std.stderr)
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	root.AddCommand(
		newServeCommand(std),
		newStatusCommand(std),
		newToolsCommand(std),
		newLoginCommand(std),
		newLogoutCommand(std),
		newReconnectCommand(std),
		newConfigCommand(std),
		newVersionCommand(std),
		newHealthcheckCommand(std),
	)

	return root
}
