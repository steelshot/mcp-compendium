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
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/admin"
	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/inbound"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/oauth"
	"github.com/steelshot/mcp-compendium/internal/router"
	"github.com/steelshot/mcp-compendium/internal/server"
	"github.com/steelshot/mcp-compendium/internal/store"
)

// daemonSettings gathers every typed setting the daemon needs.
type daemonSettings struct {
	server     server.Settings
	config     config.Settings
	downstream downstream.Settings
	router     router.Settings
	store      store.Settings
}

const (
	// mcpSessionTimeout closes idle client sessions; clients transparently start a new one on their next request.
	mcpSessionTimeout = time.Hour
	mcpPath           = "/mcp"
)

var errAnonymousExposure = errors.New("refusing to serve without authentication on a non-loopback address")

func newServeCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon",
		Args:  cobra.NoArgs,
	}

	flags := cmd.Flags()
	logging.Register(flags)
	server.Register(flags)
	config.Register(flags)
	downstream.Register(flags)
	router.Register(flags)
	store.Register(flags)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		k, err := loadSettings(flags, std.environ,
			settingsModule{defaults: logging.Defaults, envKeys: logging.EnvKeys},
			settingsModule{defaults: server.Defaults, envKeys: server.EnvKeys},
			settingsModule{defaults: config.Defaults, envKeys: config.EnvKeys},
			settingsModule{defaults: downstream.Defaults, envKeys: downstream.EnvKeys},
			settingsModule{defaults: router.Defaults, envKeys: router.EnvKeys},
			settingsModule{defaults: store.Defaults, envKeys: store.EnvKeys},
		)
		if err != nil {
			return err
		}

		logSettings, err := logging.Load(k)
		if err != nil {
			return err
		}

		var settings daemonSettings
		if settings.server, err = server.Load(k); err != nil {
			return err
		}

		if settings.config, err = config.Load(k); err != nil {
			return err
		}

		if settings.downstream, err = downstream.Load(k); err != nil {
			return err
		}

		if settings.router, err = router.Load(k); err != nil {
			return err
		}

		if settings.store, err = store.Load(k); err != nil {
			return err
		}

		return runDaemon(cmd.Context(), &settings, logging.New(std.stdout, logSettings), nil)
	}

	return cmd
}

// runDaemon wires and runs the daemon until ctx is cancelled. When listening is non-nil it receives the bound address,
// which lets tests listen on port 0.
func runDaemon(ctx context.Context, settings *daemonSettings, logger *slog.Logger, listening chan<- string) error {
	loaded, err := config.Read(settings.config.Path)
	if err != nil {
		return err
	}

	if err = checkExposure(settings.server, loaded.Auth); err != nil {
		return err
	}

	if len(loaded.EnabledServers()) == 0 {
		logger.WarnContext(ctx, "no enabled servers in the configuration", slog.Any("sources", loaded.Sources))
	}

	credentials, err := store.Open(settings.store.Dir)
	if err != nil {
		return err
	}

	defer func() { _ = credentials.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	authorizer := oauth.New(credentials, loaded.Servers, nil, logger.With(slog.String("component", "oauth")))

	pool := downstream.New(loaded.Servers, settings.downstream, authorizer, logger.With(slog.String("component", "downstream")))
	defer pool.Close()

	authorizer.OnChange(func(server string) {
		go func() { _ = pool.Reconnect(ctx, server) }()
	})

	agentRouter := router.New(settings.router, pool, logger.With(slog.String("component", "router")))

	httpServer := server.New(settings.server, logger.With(slog.String("component", "http")))
	httpServer.SetReadiness(pool.Ready)

	if err = mountHandlers(httpServer, settings, loaded, pool, authorizer, agentRouter, logger); err != nil {
		return err
	}

	pool.Start(ctx)

	listener, err := httpServer.Listen(ctx)
	if err != nil {
		return err
	}

	if listening != nil {
		listening <- listener.Addr().String()
	}

	return httpServer.Serve(ctx, listener)
}

// mountHandlers protects the MCP endpoint with the user role and the admin API with the admin role.
func mountHandlers(
	httpServer *server.Server, settings *daemonSettings, loaded *config.Config, pool *downstream.Manager, authorizer *oauth.Manager,
	agentRouter *router.Router, logger *slog.Logger,
) error {
	publicURL := ""
	if settings.server.PublicURL != nil {
		publicURL = settings.server.PublicURL.String()
	}

	// Without providers the exposure check has already confined the daemon to loopback or an explicit allowAnonymous.
	authenticator, err := inbound.New(loaded.Auth, inbound.Options{
		Anonymous: len(loaded.Auth.Providers) == 0, PublicURL: publicURL, Logger: logger.With(slog.String("component", "auth")),
	})
	if err != nil {
		return err
	}

	adminHandler, err := admin.New(pool, authorizer, logger.With(slog.String("component", "admin")))
	if err != nil {
		return err
	}

	httpServer.Handle(mcpPath, authenticator.Require(config.RoleUser, mcpHandler(agentRouter.Server(), logger)))
	httpServer.Handle(admin.Path+"/", authenticator.Require(config.RoleAdmin, http.NewCrossOriginProtection().Handler(adminHandler)))

	if settings.server.PublicURL != nil {
		agentRouter.EnableLogins(router.LoginOptions{
			Logins:    authorizer,
			PublicURL: publicURL,
			Subject: func(session string) string {
				if principal, ok := authenticator.SessionPrincipal(session); ok {
					return principal.Subject
				}

				return ""
			},
			Reconnect: pool.Reconnect,
		})

		browser := authorizer.BrowserHandler(router.LoginPath, router.CallbackPath)
		httpServer.Handle("GET "+router.LoginPath+"{id}", browser)
		httpServer.Handle("GET "+router.CallbackPath, browser)
	}

	if metadata := authenticator.ResourceMetadataHandler(mcpPath); metadata != nil {
		httpServer.Handle("GET /.well-known/oauth-protected-resource"+mcpPath, metadata)
		httpServer.Handle("GET /.well-known/oauth-protected-resource", metadata)
	}

	return nil
}

func mcpHandler(mcpServer *mcp.Server, logger *slog.Logger) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Logger:         logger.With(slog.String("component", "mcp")),
		SessionTimeout: mcpSessionTimeout,
	})

	// Browsers can be tricked into posting to a local daemon; reject cross-origin requests that carry an Origin.
	return http.NewCrossOriginProtection().Handler(handler)
}

// checkExposure refuses to start a daemon that would hand downstream credentials to anyone who can reach it.
func checkExposure(settings server.Settings, auth config.Auth) error {
	if len(auth.Providers) > 0 || auth.AllowAnonymous || server.IsLoopback(settings.Listen) {
		return nil
	}

	return fmt.Errorf("%w %s: configure auth.providers, or set auth.allowAnonymous: true when a firewall or network "+
		"policy already restricts access", errAnonymousExposure, settings.Listen)
}
