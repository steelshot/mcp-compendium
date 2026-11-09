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
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/server"
)

const (
	healthcheckTimeout = 5 * time.Second
	// healthcheckInterval is the pause between probes while --wait has time left.
	healthcheckInterval = 500 * time.Millisecond
	// maxProbeBody bounds how much of a failed probe's response the error quotes, such as the readiness reason.
	maxProbeBody = 512

	flagReady = "ready"
	flagWait  = "wait"
)

var errInvalidWait = errors.New("invalid wait")

// newHealthcheckCommand probes /livez, or /readyz with --ready. The scratch image has no shell, wget or curl, so
// container health checks and scripts waiting for the daemon run the binary itself.
func newHealthcheckCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit non-zero unless the local daemon answers its liveness or readiness probe",
		Long: "Probes /livez, which answers while the daemon is responsive. --ready probes /readyz instead, which " +
			"answers once every server has been tried. --wait keeps probing until the probe succeeds or the time is up.",
		Args: cobra.NoArgs,
	}
	server.RegisterListen(cmd.Flags())
	ready := cmd.Flags().Bool(flagReady, false, "probe readiness (/readyz) instead of liveness (/livez)")
	wait := cmd.Flags().Duration(flagWait, 0, "keep probing for up to this long until the probe succeeds")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if *wait < 0 {
			return fmt.Errorf("%w: --%s must not be negative", errInvalidWait, flagWait)
		}

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

		path := "/livez"
		if *ready {
			path = "/readyz"
		}

		return probeUntil(cmd.Context(), base+path, *wait)
	}

	return cmd
}

// probeUntil probes target until it succeeds or wait has passed, and returns the last failure.
func probeUntil(ctx context.Context, target string, wait time.Duration) error {
	deadline := time.Now().Add(wait)

	for {
		err := probe(ctx, target)
		if err == nil || time.Now().Add(healthcheckInterval).After(deadline) {
			return err
		}

		select {
		case <-ctx.Done():
			return err
		case <-time.After(healthcheckInterval):
		}
	}
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

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		if reason := strings.TrimSpace(string(body)); reason != "" {
			return fmt.Errorf("%w: %s returned %s: %s", errUnhealthy, target, resp.Status, reason)
		}

		return fmt.Errorf("%w: %s returned %s", errUnhealthy, target, resp.Status)
	}

	return nil
}
