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

// Package oauth authorizes the daemon against OAuth-protected downstream servers. Logins run in the daemon around the
// go-sdk authorization-code handler, which performs discovery, client registration, PKCE and the code exchange; the
// authorization code arrives later from the CLI's loopback listener or the daemon's own callback page. Credentials are
// persisted in the store, bound to the server URL, and refreshed transparently on use.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/store"
)

// Manager owns logins and credentials for every configured server.
type Manager struct {
	store    *store.Store
	servers  map[string]*config.Server
	client   *http.Client
	logger   *slog.Logger
	now      func() time.Time
	onChange func(server string)

	mu      sync.Mutex
	logins  map[string]*Login
	byState map[string]*Login
	sources map[string]*persistingSource
}

// bearerTransport adds the server's OAuth access token to each request, refreshing it when it has expired.
type bearerTransport struct {
	manager *Manager
	server  *config.Server
	next    http.RoundTripper
}

// persistingSource refreshes tokens and saves rotated ones, so a provider that rotates refresh tokens never strands the
// daemon with a spent one after a restart.
type persistingSource struct {
	mu         sync.Mutex
	base       oauth2.TokenSource
	credential *store.Credential
	store      *store.Store
	logger     *slog.Logger
}

var (
	errUnknownServer = errors.New("unknown server")
	errNotRemote     = errors.New("OAuth applies only to http and sse servers")
)

// New returns a manager for servers using credentials from s. The HTTP client, when nil, defaults to
// http.DefaultClient and is used for discovery, registration and token requests.
func New(s *store.Store, servers []*config.Server, client *http.Client, logger *slog.Logger) *Manager {
	if client == nil {
		client = http.DefaultClient
	}

	byName := make(map[string]*config.Server, len(servers))
	for _, server := range servers {
		byName[server.Name] = server
	}

	return &Manager{
		store: s, servers: byName, client: client, logger: logger, now: time.Now, onChange: func(string) {},
		logins: make(map[string]*Login), byState: make(map[string]*Login), sources: make(map[string]*persistingSource),
	}
}

// OnChange registers fn to run after a server's credentials change, typically to reconnect it.
func (m *Manager) OnChange(fn func(server string)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onChange = fn
}

// RoundTripper implements downstream.Authorizer.
func (m *Manager) RoundTripper(server *config.Server, next http.RoundTripper) http.RoundTripper {
	return &bearerTransport{manager: m, server: server, next: next}
}

// HasCredential reports whether a credential bound to the server's current URL exists.
func (m *Manager) HasCredential(server string) bool {
	return m.source(server) != nil
}

// Logout deletes the server's credential and reconnects it.
func (m *Manager) Logout(server string) (bool, error) {
	if _, ok := m.servers[server]; !ok {
		return false, fmt.Errorf("%w %q", errUnknownServer, server)
	}

	deleted, err := m.store.Delete(server)
	if err != nil {
		return false, fmt.Errorf("delete credential: %w", err)
	}

	m.invalidate(server)

	return deleted, nil
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	//nolint:contextcheck // token sources refresh with a long-lived context, not the triggering request's
	source := t.manager.source(t.server.Name)
	if source == nil {
		return t.next.RoundTrip(req) //nolint:wrapcheck // transports must return the underlying error unchanged
	}

	token, err := source.Token()
	if err != nil {
		// Send the request unauthenticated; the server's 401 marks the connection as needing a login.
		t.manager.logger.WarnContext(req.Context(), "token refresh failed", slog.String("server", t.server.Name), slog.String("error", err.Error()))

		return t.next.RoundTrip(req) //nolint:wrapcheck // transports must return the underlying error unchanged
	}

	req = req.Clone(req.Context())
	token.SetAuthHeader(req)

	return t.next.RoundTrip(req) //nolint:wrapcheck // transports must return the underlying error unchanged
}

func (s *persistingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err := s.base.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	if s.credential.Token == nil || token.AccessToken != s.credential.Token.AccessToken {
		s.credential.Token = token
		if err = s.store.Put(s.credential); err != nil {
			s.logger.Warn(
				"persist refreshed token failed",
				slog.String("server", s.credential.Server),
				slog.String("error", err.Error()),
			)
		}
	}

	return token, nil
}

// source returns the cached token source for server, or nil when no credential matches its current URL.
func (m *Manager) source(server string) *persistingSource {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cached, ok := m.sources[server]; ok {
		return cached
	}

	configured, ok := m.servers[server]
	if !ok || configured.URL == nil {
		return nil
	}

	credential, ok := m.store.Get(server)
	if !ok || credential.Token == nil || credential.ServerURL != configured.URL.String() {
		return nil
	}

	settings := &oauth2.Config{
		ClientID:     credential.ClientID,
		ClientSecret: credential.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: credential.AuthURL, TokenURL: credential.TokenURL, AuthStyle: credential.AuthStyle},
		RedirectURL:  credential.RedirectURL,
		Scopes:       credential.Scopes,
	}

	// The refresh context must outlive any single request; it only carries the HTTP client.
	refreshCtx := context.WithValue(context.Background(), oauth2.HTTPClient, m.client)
	cached := &persistingSource{
		base: settings.TokenSource(refreshCtx, credential.Token), credential: credential, store: m.store, logger: m.logger,
	}
	m.sources[server] = cached

	return cached
}

// invalidate drops the cached token source and notifies the listener.
func (m *Manager) invalidate(server string) {
	m.mu.Lock()
	delete(m.sources, server)
	onChange := m.onChange
	m.mu.Unlock()

	onChange(server)
}
