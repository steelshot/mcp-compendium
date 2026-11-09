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
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/schema"
)

const (
	flagCost = "cost"
	// defaultHashCost takes roughly a quarter of a second per check on current hardware; verified credentials are
	// cached by the daemon, so only the first request of a client pays for it.
	defaultHashCost   = 12
	minPasswordLength = 12
)

var (
	errInvalidCost  = errors.New("invalid cost")
	errWeakPassword = errors.New("password too short")
)

func newConfigCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate the configuration, print its JSON Schema, or hash a password for it",
	}

	cmd.AddCommand(newConfigValidateCommand(std), newConfigSchemaCommand(std), newConfigHashPasswordCommand(std))

	return cmd
}

func newConfigValidateCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Load and validate the configuration, resolving placeholders, and summarize it without secrets",
		Args:  cobra.MaximumNArgs(1),
	}
	config.Register(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		path := ""
		if len(args) == 1 {
			path = args[0]
		} else {
			k, err := loadSettings(cmd.Flags(), std.environ, settingsModule{defaults: config.Defaults, envKeys: config.EnvKeys})
			if err != nil {
				return err
			}

			settings, err := config.Load(k)
			if err != nil {
				return err
			}

			path = settings.Path
		}

		loaded, err := config.Read(path)
		if err != nil {
			return err
		}

		return writeSummary(std.stdout, loaded)
	}

	return cmd
}

func newConfigSchemaCommand(std streams) *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the configuration JSON Schema",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if _, err := std.stdout.Write(schema.Config); err != nil {
				return fmt.Errorf("write schema: %w", err)
			}

			return nil
		},
	}
}

func newConfigHashPasswordCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hash-password",
		Short: "Print the bcrypt hash of a password for a basic provider user's passwordHash",
		Long: "Reads the password from the first line of standard input, so it stays out of shell history and process " +
			"listings, for example: printf %s \"$PASSWORD\" | mcpcd config hash-password",
		Args: cobra.NoArgs,
	}
	cost := cmd.Flags().Int(flagCost, defaultHashCost, fmt.Sprintf("bcrypt cost, %d to %d; each step doubles the work", config.MinBcryptCost, bcrypt.MaxCost))

	cmd.RunE = func(*cobra.Command, []string) error {
		if *cost < config.MinBcryptCost || *cost > bcrypt.MaxCost {
			return fmt.Errorf("%w: --%s must be between %d and %d", errInvalidCost, flagCost, config.MinBcryptCost, bcrypt.MaxCost)
		}

		line, err := bufio.NewReader(std.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read password: %w", err)
		}

		password := strings.TrimRight(line, "\r\n")
		if len(password) < minPasswordLength {
			return fmt.Errorf("%w: use at least %d characters", errWeakPassword, minPasswordLength)
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), *cost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		if _, err = fmt.Fprintln(std.stdout, string(hash)); err != nil {
			return fmt.Errorf("write hash: %w", err)
		}

		return nil
	}

	return cmd
}

func writeSummary(w io.Writer, loaded *config.Config) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintf(table, "Configuration is valid (%d file(s)):\n", len(loaded.Sources))
	for _, source := range loaded.Sources {
		_, _ = fmt.Fprintf(table, "  %s\n", source)
	}

	_, _ = fmt.Fprintf(table, "\nServers (%d enabled of %d):\n", len(loaded.EnabledServers()), len(loaded.Servers))
	for _, server := range loaded.Servers {
		_, _ = fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n", server.Name, server.Transport, endpoint(server), notes(server))
	}

	_, _ = fmt.Fprintf(table, "\nAuthentication: anonymous=%t\n", loaded.Auth.AllowAnonymous)
	for _, provider := range loaded.Auth.Providers {
		_, _ = fmt.Fprintf(table, "  %s\t%s\n", provider.Name, provider.Type)
	}

	if err := table.Flush(); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	return nil
}

// endpoint describes where a server lives without printing credentials: arguments, user info and query strings are
// omitted because they commonly carry tokens.
func endpoint(server *config.Server) string {
	if server.Transport == config.TransportStdio {
		if len(server.Args) == 0 {
			return server.Command
		}

		return fmt.Sprintf("%s (+%d args)", server.Command, len(server.Args))
	}

	if server.URL == nil {
		return ""
	}

	safe := url.URL{Scheme: server.URL.Scheme, Host: server.URL.Host, Path: server.URL.Path}

	return safe.String()
}

func notes(server *config.Server) string {
	parts := make([]string, 0, 4)
	if !server.Enabled {
		parts = append(parts, "disabled")
	}

	if server.Isolation == config.IsolationSession {
		parts = append(parts, "isolation=session")
	}

	if server.OAuth != nil {
		parts = append(parts, "oauth")
	}

	if server.Tools.ReadOnly {
		parts = append(parts, "read-only")
	}

	return strings.Join(parts, ", ")
}
