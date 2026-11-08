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

package inbound

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// issuer is a TLS OIDC issuer that requires a bearer token for discovery and keys, like the Kubernetes API server.
type issuer struct {
	url      string
	key      *rsa.PrivateKey
	caFile   string
	token    string
	requests atomic.Int32
}

// aliceHash is the bcrypt hash of "correct-horse-battery" at the minimum cost, which keeps tests fast.
const aliceHash = "$2a$04$XgGHmbqho/NZyrUCOVLNQeEMH2WrOq6JIb02FYShZt9cviSOCosum"

func TestStaticProviders(t *testing.T) {
	t.Parallel()

	authenticator := mustNew(t, config.Auth{Providers: []*config.Provider{
		{
			Name:  "people",
			Type:  config.ProviderBasic,
			Users: []config.BasicUser{{Username: "alice", PasswordHash: aliceHash, Roles: []config.Role{config.RoleAdmin}}},
		},
		{Name: "ops", Type: config.ProviderBearer, Tokens: []config.BearerToken{{Token: strings.Repeat("t", 32), Roles: []config.Role{config.RoleUser}}}},
	}}, Options{})

	handler := authenticator.Require(config.RoleUser, whoami())

	cases := []struct {
		name   string
		header string
		status int
		body   string
	}{
		{name: "bearer", header: "Bearer " + strings.Repeat("t", 32), status: http.StatusOK, body: "ops/ops"},
		{name: "wrong bearer", header: "Bearer " + strings.Repeat("x", 32), status: http.StatusUnauthorized},
		{name: "basic lacks role", header: basic("alice", "correct-horse-battery"), status: http.StatusForbidden},
		{name: "basic again, from the verified cache", header: basic("alice", "correct-horse-battery"), status: http.StatusForbidden},
		{name: "basic wrong password", header: basic("alice", "nope"), status: http.StatusUnauthorized},
		{name: "basic unknown user", header: basic("mallory", "correct-horse-battery"), status: http.StatusUnauthorized},
		{name: "missing", status: http.StatusUnauthorized},
		{name: "unknown scheme", header: "Digest x", status: http.StatusUnauthorized},
	}

	for _, test := range cases {
		recorder := serve(handler, test.header, "")
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.body) {
			t.Errorf("%s: %d %q", test.name, recorder.Code, recorder.Body.String())
		}
	}

	challenges := serve(handler, "", "").Header().Values("WWW-Authenticate")
	if len(challenges) != 2 || !strings.HasPrefix(challenges[0], "Basic realm=") || !strings.HasPrefix(challenges[1], "Bearer realm=") {
		t.Errorf("challenges = %q", challenges)
	}

	if recorder := serve(authenticator.Require(config.RoleAdmin, whoami()), basic("alice", "correct-horse-battery"), ""); recorder.Code != http.StatusOK {
		t.Errorf("admin = %d", recorder.Code)
	}
}

func TestAnonymous(t *testing.T) {
	t.Parallel()

	handler := mustNew(t, config.Auth{AllowAnonymous: true}, Options{}).Require(config.RoleAdmin, whoami())
	if recorder := serve(handler, "", ""); recorder.Code != http.StatusOK || recorder.Body.String() != "anonymous/anonymous" {
		t.Errorf("anonymous = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestJWTProvider(t *testing.T) {
	t.Parallel()

	idp := newIssuer(t)
	authenticator := mustNew(t, config.Auth{Providers: []*config.Provider{{
		Name: "workloads", Type: config.ProviderJWT,
		JWT: &config.JWTProvider{
			Issuer: idp.url, Audiences: []string{"mcp-compendium"}, SubjectClaim: "sub",
			JWKS: config.JWKS{CAFile: idp.caFile, BearerTokenFile: writeTemp(t, "token", idp.token+"\n")},
			Grants: []config.Grant{
				{Match: map[string]string{"sub": "system:serviceaccount:dev:*"}, Roles: []config.Role{config.RoleUser}},
				{Match: map[string]string{"groups": "admins"}, Roles: []config.Role{config.RoleAdmin}},
			},
		},
	}}}, Options{PublicURL: "https://mcp.example"})

	user := authenticator.Require(config.RoleUser, whoami())
	admin := authenticator.Require(config.RoleAdmin, whoami())

	agent := idp.sign(t, map[string]any{"sub": "system:serviceaccount:dev:agent", "aud": []string{"mcp-compendium"}})
	if recorder := serve(user, "Bearer "+agent, ""); recorder.Code != http.StatusOK || recorder.Body.String() != "workloads/system:serviceaccount:dev:agent" {
		t.Fatalf("service account = %d %q", recorder.Code, recorder.Body.String())
	}

	if recorder := serve(admin, "Bearer "+agent, ""); recorder.Code != http.StatusForbidden {
		t.Errorf("service account as admin = %d", recorder.Code)
	}

	operator := idp.sign(t, map[string]any{"sub": "alice", "groups": []string{"devs", "admins"}, "aud": "mcp-compendium"})
	if recorder := serve(admin, "Bearer "+operator, ""); recorder.Code != http.StatusOK {
		t.Errorf("group admin = %d %q", recorder.Code, recorder.Body.String())
	}

	rejected := map[string]string{
		"wrong audience": idp.sign(t, map[string]any{"sub": "system:serviceaccount:dev:agent", "aud": "kubernetes"}),
		"no grant":       idp.sign(t, map[string]any{"sub": "system:serviceaccount:prod:agent", "aud": "mcp-compendium"}),
		"expired": idp.sign(
			t,
			map[string]any{"sub": "system:serviceaccount:dev:agent", "aud": "mcp-compendium", "exp": time.Now().Add(-time.Hour).Unix()},
		),
		"forged": forge(t, idp.url),
	}

	for name, token := range rejected {
		recorder := serve(user, "Bearer "+token, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d", name, recorder.Code)
		}

		if challenge := recorder.Header().
			Get("WWW-Authenticate"); challenge != `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource/mcp"` {
			t.Errorf("%s challenge = %q", name, challenge)
		}
	}

	if idp.requests.Load() == 0 {
		t.Error("issuer was never asked for discovery or keys")
	}

	recorder := httptest.NewRecorder()
	authenticator.ResourceMetadataHandler("/mcp").
		ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/oauth-protected-resource/mcp", http.NoBody))

	var metadata map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil || metadata["resource"] != "https://mcp.example/mcp" {
		t.Errorf("metadata = %s", recorder.Body.String())
	}
}

func TestSessionsBoundToPrincipal(t *testing.T) {
	t.Parallel()

	authenticator := mustNew(t, config.Auth{Providers: []*config.Provider{
		{Name: "ops", Type: config.ProviderBearer, Tokens: []config.BearerToken{
			{Subject: "alice", Token: strings.Repeat("a", 32), Roles: []config.Role{config.RoleUser}},
			{Subject: "bob", Token: strings.Repeat("b", 32), Roles: []config.Role{config.RoleUser}},
		}},
	}}, Options{})

	issuing := authenticator.Require(config.RoleUser, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(sessionHeader, "session-1")
		w.WriteHeader(http.StatusOK)
	}))

	if recorder := serve(issuing, "Bearer "+strings.Repeat("a", 32), ""); recorder.Code != http.StatusOK {
		t.Fatalf("initialize = %d", recorder.Code)
	}

	if principal, ok := authenticator.SessionPrincipal("session-1"); !ok || principal.Subject != "alice" {
		t.Fatalf("session principal = %+v", principal)
	}

	handler := authenticator.Require(config.RoleUser, whoami())
	if recorder := serve(handler, "Bearer "+strings.Repeat("b", 32), "session-1"); recorder.Code != http.StatusForbidden {
		t.Errorf("bob reusing alice's session = %d", recorder.Code)
	}

	if recorder := serve(handler, "Bearer "+strings.Repeat("a", 32), "session-1"); recorder.Code != http.StatusOK {
		t.Errorf("alice reusing her session = %d", recorder.Code)
	}
}

func TestVerifiedCredentialsExpireAndStayBounded(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	verified := newVerifiedCredentials(func() time.Time { return now })

	alice := verified.digest("alice", "correct-horse-battery")
	if verified.contains(alice) {
		t.Fatal("unverified credentials reported as verified")
	}

	verified.add(alice)

	if !verified.contains(alice) || verified.contains(verified.digest("alice", "wrong")) {
		t.Fatal("cache does not distinguish verified credentials")
	}

	now = now.Add(verifiedLifetime)
	if verified.contains(alice) {
		t.Fatal("expired credentials reported as verified")
	}

	for index := range maxVerifiedEntries + 10 {
		verified.add(verified.digest("user", strings.Repeat("x", index)))
	}

	if size := len(verified.entries); size > maxVerifiedEntries {
		t.Fatalf("cache holds %d entries, limit %d", size, maxVerifiedEntries)
	}
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	idp := &issuer{key: key, token: "service-account-token"}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idp.requests.Add(1)

		if r.Header.Get("Authorization") != "Bearer "+idp.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		var body any

		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			body = map[string]any{"issuer": idp.url, "jwks_uri": idp.url + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}
		case "/keys":
			body = jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &idp.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
		default:
			http.NotFound(w, r)

			return
		}

		if encodeErr := json.NewEncoder(w).Encode(body); encodeErr != nil {
			t.Error(encodeErr)
		}
	}))
	t.Cleanup(server.Close)

	idp.url = server.URL
	idp.caFile = writeTemp(t, "ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))

	return idp
}

func signWith(t *testing.T, key *rsa.PrivateKey, issuerURL string, claims map[string]any) string {
	t.Helper()

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}

	merged := map[string]any{"iss": issuerURL, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	maps.Copy(merged, claims)

	token, err := jwt.Signed(signer).Claims(merged).Serialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

// forge signs a token for the right issuer with a key the issuer never published.
func forge(t *testing.T, issuerURL string) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	return signWith(t, key, issuerURL, map[string]any{"sub": "system:serviceaccount:dev:agent", "aud": "mcp-compendium"})
}

func mustNew(t *testing.T, auth config.Auth, options Options) *Authenticator {
	t.Helper()

	authenticator, err := New(auth, options)
	if err != nil {
		t.Fatal(err)
	}

	return authenticator
}

func whoami() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := PrincipalFromContext(r.Context())
		_, _ = w.Write([]byte(principal.Provider + "/" + principal.Subject))
	})
}

func serve(handler http.Handler, authorization, session string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", http.NoBody)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	if session != "" {
		req.Header.Set(sessionHeader, session)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

func basic(username, password string) string {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.SetBasicAuth(username, password)

	return req.Header.Get("Authorization")
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func (i *issuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()

	return signWith(t, i.key, i.url, claims)
}
