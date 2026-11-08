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

package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/store"
)

// Login is an authorization in progress. The user opens AuthorizationURL; the authorization server redirects to
// RedirectURL, and whoever receives that redirect hands the code to Complete.
type Login struct {
	ID               string
	Server           string
	AuthorizationURL string
	RedirectURL      string
	ExpiresAt        time.Time
	// Subject is the principal that started the login, when known.
	Subject string

	state    string
	codes    chan codeResult
	finished chan struct{}
	cancel   context.CancelFunc

	mu   sync.Mutex
	used bool
	err  error
}

// codeResult is what the authorization server's redirect delivered.
type codeResult struct {
	result *auth.AuthorizationResult
	err    error
}

const (
	// loginTimeout bounds how long a started login waits for the user.
	loginTimeout = 10 * time.Minute
	// challengeBody is a minimal MCP initialization request, sent only to provoke the server's 401 challenge.
	challengeBody = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{},"clientInfo":{"name":"` + app.Name + `","version":"login"}}}`
)

var (
	// ErrLoginNotFound reports an unknown, expired or already used login.
	ErrLoginNotFound = errors.New("login not found or expired")
	// ErrNotRequired reports a server that answered without asking for authorization.
	ErrNotRequired = errors.New("the server did not ask for authorization")
	// ErrStateMismatch reports a callback whose state does not belong to the login, a sign of a forged redirect.
	ErrStateMismatch = errors.New("state does not match the login")
	// ErrDenied reports an authorization server error such as access_denied.
	ErrDenied = errors.New("authorization was not granted")

	errNoState = errors.New("authorization URL has no state parameter")
)

// Done is closed when the login finishes.
func (l *Login) Done() <-chan struct{} {
	return l.finished
}

// Err returns the outcome once Done is closed.
func (l *Login) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.err
}

// StartLogin begins an authorization for server whose redirect goes to redirectURL. It returns once the authorization
// URL is known, after discovery and client registration; the rest of the flow waits for Complete.
func (m *Manager) StartLogin(ctx context.Context, server, redirectURL, subject string) (*Login, error) {
	configured, ok := m.servers[server]
	if !ok {
		return nil, fmt.Errorf("%w %q", errUnknownServer, server)
	}

	if configured.URL == nil {
		return nil, fmt.Errorf("%s: %w", server, errNotRemote)
	}

	req, resp, err := m.challenge(ctx, configured) //nolint:bodyclose // the SDK handler's Authorize closes the response
	if err != nil {
		return nil, err
	}

	login := &Login{
		ID: rand.Text(), Server: server, RedirectURL: redirectURL, ExpiresAt: m.now().Add(loginTimeout), Subject: subject,
		codes: make(chan codeResult, 1), finished: make(chan struct{}),
	}

	loginCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginTimeout)
	login.cancel = cancel
	urls := make(chan string, 1)

	handler, err := auth.NewAuthorizationCodeHandler(m.handlerConfig(configured, login, urls))
	if err != nil {
		cancel()
		_ = resp.Body.Close()

		return nil, fmt.Errorf("configure authorization: %w", err)
	}

	go func() {
		err := handler.Authorize(loginCtx, req, resp)
		m.finish(login, err)
	}()

	select {
	case authorizationURL := <-urls:
		return m.register(login, authorizationURL)
	case <-login.finished:
		return nil, fmt.Errorf("start login for %s: %w", server, login.Err())
	case <-ctx.Done():
		cancel()

		return nil, fmt.Errorf("start login for %s: %w", server, ctx.Err())
	}
}

// Complete delivers the authorization server's redirect parameters to the login with id and waits for the token
// exchange to finish.
func (m *Manager) Complete(ctx context.Context, id string, redirect url.Values) error {
	m.mu.Lock()
	login := m.logins[id]
	m.mu.Unlock()

	return m.deliver(ctx, login, redirect)
}

// CompleteRedirect handles a redirect that reached the daemon's own callback, finding the login by its state.
func (m *Manager) CompleteRedirect(ctx context.Context, redirect url.Values) (*Login, error) {
	m.mu.Lock()
	login := m.byState[redirect.Get("state")]
	m.mu.Unlock()

	return login, m.deliver(ctx, login, redirect)
}

// Pending returns the login with id while it is still waiting for the user.
func (m *Manager) Pending(id string) (*Login, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	login, ok := m.logins[id]
	if !ok || m.now().After(login.ExpiresAt) {
		return nil, false
	}

	return login, true
}

func (m *Manager) deliver(ctx context.Context, login *Login, redirect url.Values) error {
	if login == nil || !login.claim() || m.now().After(login.ExpiresAt) {
		return ErrLoginNotFound
	}

	if redirect.Get("state") != login.state {
		login.cancel()

		return ErrStateMismatch
	}

	delivered := codeResult{result: &auth.AuthorizationResult{
		Code: redirect.Get("code"), State: redirect.Get("state"), Iss: redirect.Get("iss"),
	}}
	if failure := redirect.Get("error"); failure != "" {
		delivered = codeResult{err: fmt.Errorf("%w: %s %s", ErrDenied, failure, redirect.Get("error_description"))}
	}

	login.codes <- delivered

	select {
	case <-login.finished:
		return login.Err()
	case <-ctx.Done():
		return fmt.Errorf("complete login: %w", ctx.Err())
	}
}

func (m *Manager) handlerConfig(server *config.Server, login *Login, urls chan<- string) *auth.AuthorizationCodeHandlerConfig {
	preregistered, dynamic := m.registration(server, login.RedirectURL)

	handlerConfig := &auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: preregistered,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              app.Name,
			RedirectURIs:            []string{login.RedirectURL},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:         login.RedirectURL,
		RequestRefreshToken: true,
		Client:              m.client,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			urls <- args.URL

			select {
			case delivered := <-login.codes:
				return delivered.result, delivered.err
			case <-ctx.Done():
				return nil, fmt.Errorf("waiting for the user: %w", ctx.Err())
			}
		},
		NewTokenSource: func(_ context.Context, settings *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			credential := &store.Credential{
				Server: server.Name, ServerURL: server.URL.String(), ClientID: settings.ClientID, ClientSecret: settings.ClientSecret,
				AuthURL: settings.Endpoint.AuthURL, TokenURL: settings.Endpoint.TokenURL, AuthStyle: settings.Endpoint.AuthStyle,
				RedirectURL: login.RedirectURL, Scopes: settings.Scopes, Dynamic: dynamic && preregistered == nil, Token: token,
			}
			if err := m.store.Put(credential); err != nil {
				return nil, fmt.Errorf("save credential: %w", err)
			}

			return oauth2.StaticTokenSource(token), nil
		},
	}

	if server.OAuth != nil && len(server.OAuth.Scopes) > 0 {
		scopes := server.OAuth.Scopes
		handlerConfig.ScopeFilter = func([]string) []string { return scopes }
	}

	return handlerConfig
}

// registration picks the client registration: the configured client, else a dynamically registered client from an earlier
// login with the same redirect URL, else nil so the handler registers a new one. The boolean reports whether dynamic
// registration is in play.
func (m *Manager) registration(server *config.Server, redirectURL string) (*oauthex.ClientCredentials, bool) {
	if server.OAuth != nil && server.OAuth.ClientID != "" {
		credentials := &oauthex.ClientCredentials{ClientID: server.OAuth.ClientID}
		if server.OAuth.ClientSecret != "" {
			credentials.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: server.OAuth.ClientSecret}
		}

		return credentials, false
	}

	previous, ok := m.store.Get(server.Name)
	if ok && previous.Dynamic && previous.RedirectURL == redirectURL && previous.ServerURL == server.URL.String() {
		credentials := &oauthex.ClientCredentials{ClientID: previous.ClientID}
		if previous.ClientSecret != "" {
			credentials.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: previous.ClientSecret}
		}

		// Reused dynamic clients are stored as dynamic again so later logins keep reusing them.
		return credentials, true
	}

	return nil, true
}

// challenge sends an unauthenticated request so the server answers with the 401 and WWW-Authenticate header that the
// handler's discovery starts from.
func (m *Manager) challenge(ctx context.Context, server *config.Server) (*http.Request, *http.Response, error) {
	method, body := http.MethodPost, io.Reader(bytes.NewReader([]byte(challengeBody)))
	if server.Transport == config.TransportSSE {
		method, body = http.MethodGet, http.NoBody
	}

	req, err := http.NewRequestWithContext(ctx, method, server.URL.String(), body)
	if err != nil {
		return nil, nil, fmt.Errorf("build challenge request: %w", err)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("contact %s: %w", server.Name, err)
	}

	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		_ = resp.Body.Close()

		return nil, nil, fmt.Errorf("%s answered %s: %w", server.Name, resp.Status, ErrNotRequired)
	}

	return req, resp, nil
}

// register records a login once its authorization URL, and therefore its state, is known.
func (m *Manager) register(login *Login, authorizationURL string) (*Login, error) {
	parsed, err := url.Parse(authorizationURL)
	if err != nil || parsed.Query().Get("state") == "" {
		login.cancel()

		return nil, fmt.Errorf("%w: %q", errNoState, authorizationURL)
	}

	login.AuthorizationURL = authorizationURL
	login.state = parsed.Query().Get("state")

	m.mu.Lock()
	m.logins[login.ID] = login
	m.byState[login.state] = login
	m.mu.Unlock()

	return login, nil
}

func (m *Manager) finish(login *Login, err error) {
	login.cancel()

	login.mu.Lock()
	login.err = err
	login.mu.Unlock()

	m.mu.Lock()
	delete(m.logins, login.ID)
	delete(m.byState, login.state)
	m.mu.Unlock()

	close(login.finished)

	if err != nil {
		m.logger.Warn(
			"login failed",
			slog.String("server", login.Server),
			slog.String("error", err.Error()),
		)

		return
	}

	m.logger.Info("login succeeded", slog.String("server", login.Server))
	m.invalidate(login.Server)
}

// claim marks the login used, so a redirect can be delivered only once.
func (l *Login) claim() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.used {
		return false
	}

	l.used = true

	return true
}
