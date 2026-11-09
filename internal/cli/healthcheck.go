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
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/server"
)

const healthcheckTimeout = 5 * time.Second

// newHealthcheckCommand probes /livez. The scratch image has no shell, wget or curl, so container health checks run
// the binary itself.
func newHealthcheckCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit non-zero unless the local daemon answers its liveness probe",
		Args:  cobra.NoArgs,
	}
	server.RegisterListen(cmd.Flags())

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		k, err := loadSettings(cmd.Flags(), std.environ, settingsModule{defaults: server.Defaults, envKeys: server.EnvKeys})
		if err != nil {
			return err
		}

		settings, err := server.Load(k)
		if err != nil {
			return err
		}

		base, err := server.LocalURL(settings.Listen)
		if err != nil {
			return err
		}

		return probe(cmd.Context(), base+"/livez")
	}

	return cmd
}

func probe(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s returned %s", errUnhealthy, target, resp.Status)
	}

	return nil
}
