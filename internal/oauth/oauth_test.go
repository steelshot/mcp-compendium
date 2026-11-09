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
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/store"
	"github.com/steelshot/mcp-compendium/internal/testmcp"
)

const redirectURL = "http://127.0.0.1:33418/oauth/callback"

func TestLoginConnectRefreshLogout(t *testing.T) {
	t.Parallel()

	fake := testmcp.NewAuthServer(t)
	server := &config.Server{Name: "secure", Transport: config.TransportHTTP, Enabled: true, URL: mustParse(t, fake.MCPURL())}
	credentials, manager := newManager(t, server)

	login, err := manager.StartLogin(t.Context(), "secure", redirectURL, "tester")
	if err != nil {
		t.Fatalf("start login: %v", err)
	}

	if err = manager.Complete(t.Context(), login.ID, followAuthorize(t, login.AuthorizationURL)); err != nil {
		t.Fatalf("complete login: %v", err)
	}

	stored, ok := credentials.Get("secure")
	if !ok || stored.ClientID != "client-1" || !stored.Dynamic || stored.Token.RefreshToken == "" || !manager.HasCredential("secure") {
		t.Fatalf("stored credential = %+v", stored)
	}

	// The downstream connection authenticates with the stored token.
	pool := downstream.New(
		[]*config.Server{server},
		downstream.Settings{StartupTimeout: 10 * time.Second, ExecutionTimeout: 10 * time.Second},
		manager,
		logging.Discard(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); pool.Close() })
	pool.Start(ctx)

	if err = pool.WaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}

	if snapshot := pool.Snapshots()[0]; snapshot.Status != downstream.StatusOK {
		t.Fatalf("status = %s: %s", snapshot.Status, snapshot.LastError)
	}

	// Expire the access token: the next request refreshes it and persists the rotated refresh token.
	expireToken(t, credentials, manager, "secure")

	if _, err = pool.Call(t.Context(), &downstream.CallRequest{Server: "secure", Tool: "whoami", Retryable: true}); err != nil {
		t.Fatalf("call after expiry: %v", err)
	}

	if refreshed, _ := credentials.Get("secure"); refreshed.Token.RefreshToken == stored.Token.RefreshToken {
		t.Error("rotated refresh token was not persisted")
	}

	// A second login reuses the dynamically registered client.
	second, err := manager.StartLogin(t.Context(), "secure", redirectURL, "tester")
	if err != nil {
		t.Fatal(err)
	}

	if err = manager.Complete(t.Context(), second.ID, followAuthorize(t, second.AuthorizationURL)); err != nil {
		t.Fatal(err)
	}

	if fake.Clients() != 1 {
		t.Errorf("registered %d clients, want the first one reused", fake.Clients())
	}

	if deleted, logoutErr := manager.Logout("secure"); logoutErr != nil || !deleted || manager.HasCredential("secure") {
		t.Fatalf("logout = %t, %v", deleted, logoutErr)
	}
}

func TestLoginRejectsForgedAndRepeatedRedirects(t *testing.T) {
	t.Parallel()

	fake := testmcp.NewAuthServer(t)
	server := &config.Server{Name: "secure", Transport: config.TransportHTTP, Enabled: true, URL: mustParse(t, fake.MCPURL())}
	_, manager := newManager(t, server)

	login, err := manager.StartLogin(t.Context(), "secure", redirectURL, "")
	if err != nil {
		t.Fatal(err)
	}

	forged := followAuthorize(t, login.AuthorizationURL)
	forged.Set("state", "forged")

	if err = manager.Complete(t.Context(), login.ID, forged); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("forged state = %v", err)
	}

	if err = manager.Complete(t.Context(), login.ID, forged); !errors.Is(err, ErrLoginNotFound) {
		t.Fatalf("second delivery = %v", err)
	}

	denied, err := manager.StartLogin(t.Context(), "secure", redirectURL, "")
	if err != nil {
		t.Fatal(err)
	}

	redirect := url.Values{"error": {"access_denied"}, "state": {denied.state}}
	if _, err = manager.CompleteRedirect(t.Context(), redirect); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied = %v", err)
	}
}

func TestLoginNotRequired(t *testing.T) {
	t.Parallel()

	open := testmcp.ServeHTTP(t, testmcp.NewServer("open", "", "", testmcp.Tool{Name: "x"}))
	server := &config.Server{Name: "open", Transport: config.TransportHTTP, Enabled: true, URL: mustParse(t, open)}
	_, manager := newManager(t, server)

	if _, err := manager.StartLogin(t.Context(), "open", redirectURL, ""); !errors.Is(err, ErrNotRequired) {
		t.Fatalf("err = %v, want ErrNotRequired", err)
	}
}

func TestCredentialBoundToURL(t *testing.T) {
	t.Parallel()

	server := &config.Server{Name: "moved", Transport: config.TransportHTTP, Enabled: true, URL: mustParse(t, "https://new.example/mcp")}
	credentials, manager := newManager(t, server)

	if err := credentials.Put(
		&store.Credential{Server: "moved", ServerURL: "https://old.example/mcp", Token: &oauth2.Token{AccessToken: "stale"}},
	); err != nil {
		t.Fatal(err)
	}

	if manager.HasCredential("moved") {
		t.Error("a credential issued for another URL must not be used")
	}
}

func newManager(t *testing.T, servers ...*config.Server) (*store.Store, *Manager) {
	t.Helper()

	credentials, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = credentials.Close() })

	return credentials, New(credentials, servers, nil, logging.Discard())
}

// followAuthorize plays the browser and returns the redirect parameters.
func followAuthorize(t *testing.T, authorizationURL string) url.Values {
	t.Helper()

	return testmcp.Authorize(t, authorizationURL).Query()
}

// expireToken backdates the stored access token and drops the cached source so the next request refreshes it.
func expireToken(t *testing.T, credentials *store.Store, manager *Manager, server string) {
	t.Helper()

	credential, _ := credentials.Get(server)
	credential.Token.Expiry = time.Now().Add(-time.Minute)

	if err := credentials.Put(credential); err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	delete(manager.sources, server)
	manager.mu.Unlock()
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	return parsed
}
