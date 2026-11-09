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
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cli/browser"
	"github.com/spf13/cobra"

	"github.com/steelshot/mcp-compendium/internal/admin/gen"
	"github.com/steelshot/mcp-compendium/internal/client"
	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/oauth"
	"github.com/steelshot/mcp-compendium/internal/store"
)

// loginBackend runs the daemon side of a login: remotely through the admin API, or in-process with --local.
type loginBackend interface {
	start(ctx context.Context, server, redirectURL string) (loginTicket, error)
	complete(ctx context.Context, id string, redirect url.Values) error
}

// loginTicket identifies a started login.
type loginTicket struct {
	id               string
	authorizationURL string
}

// remoteLogin drives a login on a running daemon.
type remoteLogin struct {
	api *gen.Client
}

// localLogin drives a login in-process against the state directory, for use without a running daemon.
type localLogin struct {
	manager *oauth.Manager
}

const (
	defaultCallbackPort = 33418
	callbackPath        = "/oauth/callback"
	loginWait           = 10 * time.Minute
	callbackTimeout     = 10 * time.Second
	flagCallbackPort    = "callback-port"
	flagNoBrowser       = "no-browser"
	flagLocal           = "local"
)

var errNoRedirect = errors.New("no authorization redirect received")

func newLoginCommand(std streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <server>",
		Short: "Authorize the daemon against an OAuth-protected server",
		Long: "Starts an OAuth login on the daemon and opens the authorization page in a browser. The authorization server " +
			"redirects to a listener on this machine, which hands the result to the daemon; the daemon exchanges it and " +
			"stores the tokens. Pre-registered OAuth clients must allow the redirect URI http://127.0.0.1:<callback-port>" +
			callbackPath + ".",
		Args: exactlyOneServer,
	}

	flags := cmd.Flags()
	client.Register(flags)
	config.Register(flags)
	store.Register(flags)
	flags.Int(flagCallbackPort, defaultCallbackPort, "loopback port for the authorization redirect")
	flags.Bool(flagNoBrowser, false, "print the authorization URL instead of opening a browser, and accept a pasted redirect URL")
	flags.Bool(flagLocal, false, "log in without a running daemon, writing the state directory directly")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		port, _ := flags.GetInt(flagCallbackPort)
		noBrowser, _ := flags.GetBool(flagNoBrowser)
		local, _ := flags.GetBool(flagLocal)

		backend, closeBackend, err := newLoginBackend(cmd, std, local)
		if err != nil {
			return err
		}

		defer closeBackend()

		return runLogin(cmd.Context(), std, backend, args[0], port, noBrowser)
	}

	return cmd
}

func newLoginBackend(cmd *cobra.Command, std streams, local bool) (loginBackend, func(), error) {
	if !local {
		api, err := adminClient(cmd, std)

		return remoteLogin{api: api}, func() {}, err
	}

	k, err := loadSettings(cmd.Flags(), std.environ,
		settingsModule{defaults: config.Defaults, envKeys: config.EnvKeys},
		settingsModule{defaults: store.Defaults, envKeys: store.EnvKeys},
	)
	if err != nil {
		return nil, nil, err
	}

	configSettings, err := config.Load(k)
	if err != nil {
		return nil, nil, err
	}

	storeSettings, err := store.Load(k)
	if err != nil {
		return nil, nil, err
	}

	loaded, err := config.Read(configSettings.Path)
	if err != nil {
		return nil, nil, err
	}

	credentials, err := store.Open(storeSettings.Dir)
	if err != nil {
		return nil, nil, err
	}

	manager := oauth.New(credentials, loaded.Servers, nil, logging.Discard())

	return localLogin{manager: manager}, func() { _ = credentials.Close() }, nil
}

// runLogin listens for the redirect, starts the login, sends the user to the authorization page and completes the
// login with whichever arrives first: the redirect, or a redirect URL pasted on stdin.
func runLogin(ctx context.Context, std streams, backend loginBackend, server string, port int, noBrowser bool) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen for the authorization redirect on port %d (choose another with --%s): %w", port, flagCallbackPort, err)
	}

	redirects := make(chan url.Values, 1)
	callback := &http.Server{Handler: callbackHandler(redirects), ReadHeaderTimeout: callbackTimeout}

	go func() { _ = callback.Serve(listener) }()

	defer func() { _ = callback.Close() }()

	redirectURL := "http://" + listener.Addr().String() + callbackPath

	ticket, err := backend.start(ctx, server, redirectURL)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(std.stderr, "Authorize %s in your browser:\n\n  %s\n\n", server, ticket.authorizationURL)

	if noBrowser || openBrowser(ticket.authorizationURL) != nil {
		_, _ = fmt.Fprintln(std.stderr, "If the browser runs on another machine, paste the address it is redirected to here and press Enter.")

		go readPastedRedirect(std, redirects)
	}

	var redirect url.Values

	select {
	case redirect = <-redirects:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errNoRedirect, ctx.Err())
	}

	if err = backend.complete(ctx, ticket.id, redirect); err != nil {
		return err
	}

	_, err = fmt.Fprintf(std.stdout, "Logged in to %s\n", server)

	return wrapWrite(err)
}

// callbackHandler answers the browser and forwards the first redirect it receives.
func callbackHandler(redirects chan<- url.Values) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+callbackPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		select {
		case redirects <- query:
		default:
		}

		message := "Authorization received. You can close this tab and return to the terminal."
		if failure := query.Get("error"); failure != "" {
			message = "Authorization failed: " + failure + " " + query.Get("error_description")
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<!doctype html><title>mcpcd login</title><p>%s</p>", html.EscapeString(message))
	})

	return mux
}

func readPastedRedirect(std streams, redirects chan<- url.Values) {
	if std.stdin == nil {
		return
	}

	scanner := bufio.NewScanner(std.stdin)
	for scanner.Scan() {
		pasted, err := url.Parse(strings.TrimSpace(scanner.Text()))
		if err != nil || pasted.Query().Get("state") == "" {
			_, _ = fmt.Fprintln(std.stderr, "That is not a redirect URL with a state parameter; paste the full address.")

			continue
		}

		select {
		case redirects <- pasted.Query():
		default:
		}

		return
	}
}

// openBrowser asks the desktop to open target. The launcher's own output (xdg-open is chatty) is discarded so that it
// cannot interleave with the instructions printed to the terminal.
func openBrowser(target string) error {
	browser.Stdout, browser.Stderr = io.Discard, io.Discard

	if err := browser.OpenURL(target); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	return nil
}

func (r remoteLogin) start(ctx context.Context, server, redirectURL string) (loginTicket, error) {
	login, err := r.api.StartLogin(ctx, &gen.LoginRequest{RedirectUri: redirectURL}, gen.StartLoginParams{Server: server})
	if err != nil {
		return loginTicket{}, client.Describe(err)
	}

	return loginTicket{id: login.ID, authorizationURL: login.AuthorizationUrl}, nil
}

func (r remoteLogin) complete(ctx context.Context, id string, redirect url.Values) error {
	callback := &gen.LoginCallback{State: redirect.Get("state")}
	fields := map[string]*gen.OptString{
		"code": &callback.Code, "iss": &callback.Iss, "error": &callback.Error, "error_description": &callback.ErrorDescription,
	}
	for key, target := range fields {
		if value := redirect.Get(key); value != "" {
			target.SetTo(value)
		}
	}

	return client.Describe(r.api.CompleteLogin(ctx, callback, gen.CompleteLoginParams{Login: id}))
}

func (l localLogin) start(ctx context.Context, server, redirectURL string) (loginTicket, error) {
	login, err := l.manager.StartLogin(ctx, server, redirectURL, "local")
	if err != nil {
		return loginTicket{}, err
	}

	return loginTicket{id: login.ID, authorizationURL: login.AuthorizationURL}, nil
}

func (l localLogin) complete(ctx context.Context, id string, redirect url.Values) error {
	return l.manager.Complete(ctx, id, redirect)
}
