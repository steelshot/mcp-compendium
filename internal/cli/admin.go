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
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/admin/gen"
	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/client"
)

var errOneServer = errors.New("expected exactly one server name")

func newStatusCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show every server's connection state",
		Args:  cobra.NoArgs,
	}
	client.Register(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		api, err := adminClient(cmd, std)
		if err != nil {
			return err
		}

		list, err := api.ListServers(cmd.Context())
		if err != nil {
			return client.Describe(err)
		}

		return writeStatus(std.stdout, list)
	}

	return cmd
}

func newToolsCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools <server>",
		Short: "List a server's tools, including those hidden by its filters",
		Args:  exactlyOneServer,
	}
	client.Register(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		api, err := adminClient(cmd, std)
		if err != nil {
			return err
		}

		detail, err := api.GetServer(cmd.Context(), gen.GetServerParams{Server: args[0]})
		if err != nil {
			return client.Describe(err)
		}

		table := tabwriter.NewWriter(std.stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "TOOL\tEXPOSED\tHINTS\tDESCRIPTION")

		for _, tool := range detail.Tools {
			description, _ := tool.Description.Get()
			_, _ = fmt.Fprintf(table, "%s\t%t\t%s\t%s\n", tool.Name, tool.Exposed, strings.Join(tool.Hints, ","), firstLine(description))
		}

		return flush(table)
	}

	return cmd
}

func newReconnectCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconnect <server>",
		Short: "Reconnect a server now, ignoring any cooldown",
		Args:  exactlyOneServer,
	}
	client.Register(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		api, err := adminClient(cmd, std)
		if err != nil {
			return err
		}

		status, err := api.ReconnectServer(cmd.Context(), gen.ReconnectServerParams{Server: args[0]})
		if err != nil {
			return client.Describe(err)
		}

		return writeStatus(std.stdout, &gen.ServerList{Version: "", Servers: []gen.ServerStatus{*status}})
	}

	return cmd
}

func newLogoutCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout <server>",
		Short: "Delete a server's stored OAuth credential",
		Args:  exactlyOneServer,
	}
	client.Register(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		api, err := adminClient(cmd, std)
		if err != nil {
			return err
		}

		result, err := api.Logout(cmd.Context(), gen.LogoutParams{Server: args[0]})
		if err != nil {
			return client.Describe(err)
		}

		message := "No credential was stored for " + args[0]
		if result.Deleted {
			message = "Logged out of " + args[0]
		}

		_, err = fmt.Fprintln(std.stdout, message)

		return wrapWrite(err)
	}

	return cmd
}

func adminClient(cmd *cobra.Command, std streams) (*gen.Client, error) {
	k, err := loadSettings(cmd.Flags(), std.environ, settingsModule{defaults: client.Defaults, envKeys: client.EnvKeys})
	if err != nil {
		return nil, err
	}

	settings, err := client.Load(k)
	if err != nil {
		return nil, err
	}

	return client.New(settings)
}

func writeStatus(w io.Writer, list *gen.ServerList) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "SERVER\tTRANSPORT\tSTATUS\tTOOLS\tAUTH\tDETAIL")

	for index := range list.Servers {
		server := &list.Servers[index]
		tools := strconv.Itoa(server.Tools)
		if server.Filtered > 0 {
			tools = fmt.Sprintf("%d (+%d hidden)", server.Tools, server.Filtered)
		}

		authenticated := "-"
		if server.Authenticated {
			authenticated = "oauth"
		}

		detail, _ := server.LastError.Get()
		if server.Status == gen.ServerStatusStatusUnauthorized {
			detail = "run: " + app.Binary + " login " + server.Name
		}

		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", server.Name, server.Transport, server.Status, tools, authenticated, firstLine(detail))
	}

	if list.Version != "" {
		_, _ = fmt.Fprintf(table, "\ndaemon %s\n", list.Version)
	}

	return flush(table)
}

func exactlyOneServer(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return errOneServer
	}

	return nil
}

func firstLine(text string) string {
	const maxWidth = 100

	text, _, _ = strings.Cut(text, "\n")
	if len(text) > maxWidth {
		text = text[:maxWidth] + "…"
	}

	return text
}

func flush(table *tabwriter.Writer) error {
	return wrapWrite(table.Flush())
}

func wrapWrite(err error) error {
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// withTimeout bounds a context while keeping cancellation from the caller.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, loginWait)
}
