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

// Package inbound authenticates clients of the daemon and authorizes them by role. Basic, static bearer and JWT
// (OIDC, including Kubernetes service account tokens) providers may be enabled side by side; each grants the user role
// (the MCP endpoint), the admin role (the admin API) or both.
package inbound

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// Principal is an authenticated caller.
type Principal struct {
	Provider string
	Subject  string
	Roles    []config.Role
}

// Authenticator authenticates requests against every configured provider.
type Authenticator struct {
	basic     []*basicProvider
	bearer    []*bearerProvider
	jwt       []*jwtProvider
	anonymous bool
	sessions  *sessions
	logger    *slog.Logger
	// publicURL, when set, is the externally visible base URL used in protected resource metadata.
	publicURL string
}

// Options configure an Authenticator beyond the configuration file.
type Options struct {
	// Anonymous grants unauthenticated requests both roles.
	Anonymous bool
	// PublicURL is the externally visible base URL, without a trailing slash.
	PublicURL string
	// HTTPClient fetches OIDC discovery documents and keys; nil uses a default client.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// problem is an RFC 9457 problem document.
type problem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// principalKey is the context key for the authenticated Principal.
type principalKey struct{}

const (
	anonymousProvider = "anonymous"
	sessionHeader     = "Mcp-Session-Id"
	realm             = "mcp-compendium"
)

var (
	errInvalidCredentials = errors.New("invalid credentials")
	errNoMatchingProvider = errors.New("no provider accepts these credentials")
)

// New builds an Authenticator from the auth section of the configuration.
func New(auth config.Auth, options Options) (*Authenticator, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	a := &Authenticator{anonymous: options.Anonymous || auth.AllowAnonymous, sessions: newSessions(), logger: logger, publicURL: options.PublicURL}

	for _, provider := range auth.Providers {
		switch provider.Type {
		case config.ProviderBasic:
			built, err := newBasicProvider(provider)
			if err != nil {
				return nil, err
			}

			a.basic = append(a.basic, built)
		case config.ProviderBearer:
			a.bearer = append(a.bearer, newBearerProvider(provider))
		case config.ProviderJWT:
			built, err := newJWTProvider(provider, options.HTTPClient)
			if err != nil {
				return nil, err
			}

			a.jwt = append(a.jwt, built)
		}
	}

	return a, nil
}

// PrincipalFromContext returns the authenticated principal of a request.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*Principal)

	return principal, ok
}

// Has reports whether the principal holds role.
func (p *Principal) Has(role config.Role) bool {
	return slices.Contains(p.Roles, role)
}

// Require wraps next so that only principals with role reach it. MCP sessions are bound to the principal that created
// them, so a session ID alone cannot be replayed by another caller.
func (a *Authenticator) Require(role config.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := a.authenticate(r)
		if err != nil {
			a.logger.WarnContext(r.Context(), "authentication failed", slog.String("remote", r.RemoteAddr), slog.String("reason", err.Error()))
			a.challenge(w, r)

			return
		}

		if !principal.Has(role) {
			a.logger.WarnContext(r.Context(), "forbidden", slog.String("subject", principal.Subject), slog.String("role", string(role)))
			writeProblem(w, http.StatusForbidden, "forbidden", "requires the "+string(role)+" role")

			return
		}

		if session := r.Header.Get(sessionHeader); session != "" && !a.sessions.owns(session, principal) {
			writeProblem(w, http.StatusForbidden, "forbidden", "the session belongs to another principal")

			return
		}

		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		next.ServeHTTP(&sessionRecorder{ResponseWriter: w, sessions: a.sessions, principal: principal}, r.WithContext(ctx))

		if r.Method == http.MethodDelete {
			a.sessions.forget(r.Header.Get(sessionHeader))
		}
	})
}

// SessionPrincipal returns the principal that created an MCP session.
func (a *Authenticator) SessionPrincipal(session string) (*Principal, bool) {
	return a.sessions.principal(session)
}

// ResourceMetadataHandler serves RFC 9728 protected resource metadata naming the JWT issuers, so OAuth-capable MCP
// clients can obtain tokens from them. It returns nil when no JWT provider is configured.
func (a *Authenticator) ResourceMetadataHandler(resourcePath string) http.Handler {
	if len(a.jwt) == 0 {
		return nil
	}

	issuers := make([]string, 0, len(a.jwt))
	for _, provider := range a.jwt {
		issuers = append(issuers, provider.config.Issuer)
	}

	return resourceMetadataHandler(a.publicURL, resourcePath, issuers)
}

// writeProblem answers with an RFC 9457 problem document, matching the admin API's error format.
func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(problem{Title: title, Status: status, Detail: detail}); err != nil {
		return
	}
}

// digestEqual compares secrets in constant time regardless of their lengths.
func digestEqual(a, b string) bool {
	left, right := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))

	return subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

func (a *Authenticator) authenticate(r *http.Request) (*Principal, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		if a.anonymous {
			return &Principal{Provider: anonymousProvider, Subject: anonymousProvider, Roles: []config.Role{config.RoleUser, config.RoleAdmin}}, nil
		}

		return nil, errInvalidCredentials
	}

	if username, password, ok := r.BasicAuth(); ok {
		for _, provider := range a.basic {
			if principal := provider.authenticate(username, password); principal != nil {
				return principal, nil
			}
		}

		return nil, errInvalidCredentials
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return nil, errInvalidCredentials
	}

	for _, provider := range a.bearer {
		if principal := provider.authenticate(token); principal != nil {
			return principal, nil
		}
	}

	if strings.Count(token, ".") == 2 {
		issuer := unverifiedIssuer(token)
		for _, provider := range a.jwt {
			if provider.config.Issuer == issuer {
				return provider.authenticate(r.Context(), token)
			}
		}
	}

	return nil, errNoMatchingProvider
}

func (a *Authenticator) challenge(w http.ResponseWriter, r *http.Request) {
	if len(a.basic) > 0 {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", realm))
	}

	if len(a.jwt) > 0 {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf("Bearer resource_metadata=%q", metadataURL(a.publicURL, r)))
	} else if len(a.bearer) > 0 {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf("Bearer realm=%q", realm))
	}

	writeProblem(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credentials")
}
