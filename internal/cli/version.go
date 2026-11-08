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

	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/app"
)

func newVersionCommand(std streams) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			info := app.BuildInfo()
			if _, err := fmt.Fprintf(
				std.stdout,
				"%s %s\ncommit: %s\nbuilt:  %s\ngo:     %s\n",
				app.Binary,
				info.Version,
				info.Commit,
				info.Date,
				info.GoVersion,
			); err != nil {
				return fmt.Errorf("write version: %w", err)
			}

			return nil
		},
	}
}
