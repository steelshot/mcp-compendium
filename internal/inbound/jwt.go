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
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gobwas/glob"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// jwtProvider verifies JWTs from one issuer, discovering its keys lazily so an unreachable issuer does not stop the
// daemon from starting.
type jwtProvider struct {
	name   string
	config *config.JWTProvider
	client *http.Client
	grants []compiledGrant
	now    func() time.Time

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	retryAt  time.Time
	lastErr  error
}

// compiledGrant is a grant with its claim patterns compiled.
type compiledGrant struct {
	match map[string]*glob.Pattern
	roles []config.Role
}

// tokenFileTransport authenticates discovery and key requests with a token read from a file on every request, as
// Kubernetes rotates projected service account tokens.
type tokenFileTransport struct {
	path string
	next http.RoundTripper
}

const (
	discoveryRetry = 30 * time.Second
	jwtTimeout     = 10 * time.Second
)

var (
	errAudience = errors.New("token audience is not accepted")
	errNoGrant  = errors.New("no grant matches the token's claims")
	errSubject  = errors.New("token has no subject claim")
)

func (t tokenFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := os.ReadFile(t.path)
	if err != nil {
		return nil, fmt.Errorf("read bearer token file: %w", err)
	}

	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))

	return t.next.RoundTrip(req) //nolint:wrapcheck // transports must return the underlying error unchanged
}

func newJWTProvider(provider *config.Provider, base *http.Client) (*jwtProvider, error) {
	client, err := jwksClient(provider.JWT.JWKS, base)
	if err != nil {
		return nil, fmt.Errorf("auth provider %s: %w", provider.Name, err)
	}

	grants := make([]compiledGrant, 0, len(provider.JWT.Grants))

	for _, grant := range provider.JWT.Grants {
		compiled := compiledGrant{match: make(map[string]*glob.Pattern, len(grant.Match)), roles: grant.Roles}

		for claim, pattern := range grant.Match {
			matcher, compileErr := glob.Compile(pattern)
			if compileErr != nil {
				return nil, fmt.Errorf("auth provider %s: claim %s: %w", provider.Name, claim, compileErr)
			}

			compiled.match[claim] = matcher
		}

		grants = append(grants, compiled)
	}

	return &jwtProvider{name: provider.Name, config: provider.JWT, client: client, grants: grants, now: time.Now}, nil
}

func claimMatches(value any, pattern *glob.Pattern) bool {
	switch typed := value.(type) {
	case string:
		return pattern.Match(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && pattern.Match(text) {
				return true
			}
		}
	}

	return false
}

func jwksClient(settings config.JWKS, base *http.Client) (*http.Client, error) {
	if base == nil {
		base = &http.Client{Timeout: jwtTimeout}
	}

	if settings.CAFile == "" && settings.BearerTokenFile == "" {
		return base, nil
	}

	transport := http.DefaultTransport
	if base.Transport != nil {
		transport = base.Transport
	}

	if settings.CAFile != "" {
		trusted, err := withCA(transport, settings.CAFile)
		if err != nil {
			return nil, err
		}

		transport = trusted
	}

	if settings.BearerTokenFile != "" {
		transport = tokenFileTransport{path: settings.BearerTokenFile, next: transport}
	}

	return &http.Client{Transport: transport, Timeout: base.Timeout}, nil
}

// withCA returns a copy of transport that also trusts the certificates in caFile.
func withCA(transport http.RoundTripper, caFile string) (http.RoundTripper, error) {
	pem, err := os.ReadFile(caFile) //nolint:gosec // the path comes from the operator's configuration
	if err != nil {
		return nil, fmt.Errorf("read jwks.caFile: %w", err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("jwks.caFile %s: %w", caFile, errNoCertificates)
	}

	cloned := &http.Transport{}
	if base, ok := transport.(*http.Transport); ok {
		cloned = base.Clone()
	}

	cloned.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return cloned, nil
}

// unverifiedIssuer reads the iss claim without verifying the signature, only to route the token to its provider.
func unverifiedIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims struct {
		Issuer string `json:"iss"`
	}

	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}

	return claims.Issuer
}

func (p *jwtProvider) authenticate(ctx context.Context, raw string) (*Principal, error) {
	//nolint:contextcheck // the verifier outlives this request and fetches keys with its own long-lived context
	verifier, err := p.ensureVerifier()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, p.client), jwtTimeout)
	defer cancel()

	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	if !slices.ContainsFunc(token.Audience, func(audience string) bool { return slices.Contains(p.config.Audiences, audience) }) {
		return nil, errAudience
	}

	var claims map[string]any
	if err = token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	subject, _ := claims[p.config.SubjectClaim].(string)
	if subject == "" {
		return nil, errSubject
	}

	roles := p.roles(claims)
	if len(roles) == 0 {
		return nil, fmt.Errorf("%w (subject %s)", errNoGrant, subject)
	}

	return &Principal{Provider: p.name, Subject: subject, Roles: roles}, nil
}

// ensureVerifier builds the verifier on first use, from the configured JWKS URL or from OIDC discovery, and retries a
// failed discovery no more than every 30 seconds.
func (p *jwtProvider) ensureVerifier() (*oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.verifier != nil {
		return p.verifier, nil
	}

	if p.now().Before(p.retryAt) {
		return nil, p.lastErr
	}

	// Key sets refresh in the background for the provider's lifetime, so they need a long-lived context.
	keyCtx := oidc.ClientContext(context.Background(), p.client)
	verifierConfig := &oidc.Config{SkipClientIDCheck: true, Now: p.now}

	if p.config.JWKS.URL != "" {
		p.verifier = oidc.NewVerifier(p.config.Issuer, oidc.NewRemoteKeySet(keyCtx, p.config.JWKS.URL), verifierConfig)

		return p.verifier, nil
	}

	discoverCtx, cancel := context.WithTimeout(keyCtx, jwtTimeout)
	defer cancel()

	provider, err := oidc.NewProvider(discoverCtx, p.config.Issuer)
	if err != nil {
		p.retryAt = p.now().Add(discoveryRetry)
		p.lastErr = fmt.Errorf("discover %s: %w", p.config.Issuer, err)

		return nil, p.lastErr
	}

	// VerifierContext refreshes keys with keyCtx rather than the short-lived discovery context.
	p.verifier = provider.VerifierContext(keyCtx, verifierConfig)

	return p.verifier, nil
}

// roles unions the roles of every grant whose claim patterns all match.
func (p *jwtProvider) roles(claims map[string]any) []config.Role {
	roles := make([]config.Role, 0, 2)

	for _, grant := range p.grants {
		matched := true

		for claim, pattern := range grant.match {
			if !claimMatches(claims[claim], pattern) {
				matched = false

				break
			}
		}

		if !matched {
			continue
		}

		for _, role := range grant.roles {
			if !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
	}

	return roles
}
