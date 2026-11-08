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
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// sessions binds each MCP session ID to the principal that created it.
type sessions struct {
	mu     sync.Mutex
	owners map[string]*Principal
}

// sessionRecorder captures the session ID the MCP handler assigns in its response.
type sessionRecorder struct {
	http.ResponseWriter

	sessions  *sessions
	principal *Principal
	recorded  bool
}

var errNoCertificates = errors.New("no PEM certificates found")

func (r *sessionRecorder) WriteHeader(status int) {
	r.record()
	r.ResponseWriter.WriteHeader(status)
}

func (r *sessionRecorder) Write(data []byte) (int, error) {
	r.record()

	return r.ResponseWriter.Write(data) //nolint:wrapcheck // http.ResponseWriter contract
}

// Flush keeps server-sent event streams working through the wrapper.
func (r *sessionRecorder) Flush() {
	r.record()

	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *sessionRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func newSessions() *sessions {
	return &sessions{owners: make(map[string]*Principal)}
}

// resourceMetadataHandler serves RFC 9728 metadata for the resource at resourcePath.
func resourceMetadataHandler(publicURL, resourcePath string, issuers []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := oauthex.ProtectedResourceMetadata{
			Resource:               baseURL(publicURL, r) + resourcePath,
			AuthorizationServers:   issuers,
			BearerMethodsSupported: []string{"header"},
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")

		if err := json.NewEncoder(w).Encode(metadata); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

// metadataURL is the RFC 9728 well-known URL for the MCP endpoint.
func metadataURL(publicURL string, r *http.Request) string {
	return baseURL(publicURL, r) + "/.well-known/oauth-protected-resource" + r.URL.Path
}

func baseURL(publicURL string, r *http.Request) string {
	if publicURL != "" {
		return publicURL
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}

func (r *sessionRecorder) record() {
	if r.recorded {
		return
	}

	r.recorded = true

	if session := r.Header().Get(sessionHeader); session != "" {
		r.sessions.claim(session, r.principal)
	}
}

// owns reports whether principal may use session: unknown sessions are let through for the MCP handler to reject.
func (s *sessions) owns(session string, principal *Principal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	owner, ok := s.owners[session]

	return !ok || (owner.Provider == principal.Provider && owner.Subject == principal.Subject)
}

func (s *sessions) claim(session string, principal *Principal) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.owners[session]; !ok {
		s.owners[session] = principal
	}
}

func (s *sessions) principal(session string) (*Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	principal, ok := s.owners[session]

	return principal, ok
}

func (s *sessions) forget(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.owners, session)
}
