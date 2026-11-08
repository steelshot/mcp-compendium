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

package testmcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// AuthServer is a fake OAuth authorization server and a protected MCP endpoint on one listener. It implements protected
// resource metadata, authorization server metadata, dynamic client registration, PKCE (S256) and refresh token rotation.
type AuthServer struct {
	base    string
	mu      sync.Mutex
	codes   map[string]pendingCode
	access  map[string]bool
	refresh map[string]bool
	issued  int
	clients int
}

// pendingCode is an issued authorization code awaiting exchange.
type pendingCode struct {
	challenge, redirect string
}

const (
	grantCode    = "authorization_code"
	grantRefresh = "refresh_token"
)

// NewAuthServer starts the fake until the test ends. Its MCP endpoint, at MCPURL, exposes one tool, whoami.
func NewAuthServer(t *testing.T) *AuthServer {
	t.Helper()

	fake := &AuthServer{codes: map[string]pendingCode{}, access: map[string]bool{}, refresh: map[string]bool{}}
	mcpHandler := HTTPHandler(NewServer("secure", "", "", Tool{Name: "whoami"}))

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		valid := fake.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		fake.mu.Unlock()

		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+fake.base+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"resource": fake.base + "/mcp", "authorization_servers": []string{fake.base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": fake.base, "authorization_endpoint": fake.base + "/authorize", "token_endpoint": fake.base + "/token",
			"registration_endpoint": fake.base + "/register", "response_types_supported": []string{"code"},
			"grant_types_supported": []string{grantCode, grantRefresh}, "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("POST /register", fake.handleRegister)
	mux.HandleFunc("GET /authorize", fake.handleAuthorize)
	mux.HandleFunc("POST /token", fake.handleToken)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fake.base = server.URL

	return fake
}

// Authorize plays the browser: it opens authorizationURL and returns the redirect the server sent it to.
func Authorize(t *testing.T, authorizationURL string) *url.URL {
	t.Helper()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, authorizationURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %s %q", resp.Status, resp.Header.Get("Location"))
	}

	return location
}

// MCPURL is the protected MCP endpoint.
func (f *AuthServer) MCPURL() string {
	return f.base + "/mcp"
}

// Clients returns how many clients registered dynamically.
func (f *AuthServer) Clients() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.clients
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *AuthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var metadata map[string]any
	_ = json.NewDecoder(r.Body).Decode(&metadata)

	f.mu.Lock()
	f.clients++
	id := fmt.Sprintf("client-%d", f.clients)
	f.mu.Unlock()

	metadata["client_id"] = id
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, metadata)
}

func (f *AuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("resource") != f.base+"/mcp" {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.issued++
	code := fmt.Sprintf("code-%d", f.issued)
	f.codes[code] = pendingCode{challenge: query.Get("code_challenge"), redirect: query.Get("redirect_uri")}
	f.mu.Unlock()

	target, _ := url.Parse(query.Get("redirect_uri"))
	target.RawQuery = url.Values{"code": {code}, "state": {query.Get("state")}}.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound) //nolint:gosec // test fake: redirecting to the client's redirect_uri is its job
}

func (f *AuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Form.Get("grant_type") {
	case grantCode:
		pending, ok := f.codes[r.Form.Get("code")]
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))

		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != pending.challenge || r.Form.Get("redirect_uri") != pending.redirect {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})

			return
		}

		delete(f.codes, r.Form.Get("code"))
	case grantRefresh:
		if !f.refresh[r.Form.Get("refresh_token")] {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant"})

			return
		}

		// Rotate: the old refresh token is spent.
		delete(f.refresh, r.Form.Get("refresh_token"))
	}

	f.issued++
	access, refresh := fmt.Sprintf("access-%d", f.issued), fmt.Sprintf("refresh-%d", f.issued)
	f.access[access], f.refresh[refresh] = true, true

	writeJSON(w, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600})
}
