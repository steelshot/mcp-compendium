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

package router

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/oauth"
)

// Logins starts browser logins for servers that need authorization.
type Logins interface {
	StartLogin(ctx context.Context, server, redirectURL, subject string) (*oauth.Login, error)
}

// LoginOptions enable sign-in links sent to agents through URL-mode elicitation.
type LoginOptions struct {
	Logins Logins
	// PublicURL is the browser-reachable base URL of the daemon, without a trailing slash.
	PublicURL string
	// Subject names the principal behind an MCP session, binding a login to whoever triggered it.
	Subject func(session string) string
	// Reconnect connects a server again once its login has succeeded.
	Reconnect func(ctx context.Context, server string) error
}

// elicitedLogins tracks logins started through elicitation until their links expire.
type elicitedLogins struct {
	options LoginOptions
	key     []byte

	mu     sync.Mutex
	active map[string]*oauth.Login
}

// Browser-facing paths: sign-in links sent to agents, and the authorization server's redirect back to the daemon.
const (
	LoginPath    = "/oauth/login/"
	CallbackPath = "/oauth/callback"

	loginInput     = "login"
	actionAccept   = "accept"
	elicitationURL = "url"
)

// EnableLogins lets call_tool answer an unauthorized server with a sign-in link when the client supports URL
// elicitation, instead of only telling the agent to ask the user to run the CLI.
func (r *Router) EnableLogins(options LoginOptions) {
	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.logins = &elicitedLogins{options: options, key: key, active: make(map[string]*oauth.Login)}
}

func sessionID(req *mcp.CallToolRequest) string {
	if req.Session == nil {
		return ""
	}

	return req.Session.ID()
}

// authorize answers a call that hit an unauthorized server: with a URL elicitation when possible, otherwise with the
// error that tells the agent to ask the user to run the CLI login.
func (r *Router) authorize(ctx context.Context, req *mcp.CallToolRequest, server string, failure *toolError) *mcp.CallToolResult {
	logins := r.loginSupport(req)
	if logins == nil {
		return errorResult(failure)
	}

	session := sessionID(req)

	login, err := logins.options.Logins.StartLogin(ctx, server, logins.options.PublicURL+CallbackPath, logins.subject(session))
	if err != nil {
		failure.Message += ": " + err.Error()

		return errorResult(failure)
	}

	logins.remember(login)

	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{loginInput: &mcp.ElicitParams{
			Mode:          elicitationURL,
			Message:       fmt.Sprintf("Sign in to %s so the agent can use its tools.", server),
			URL:           logins.options.PublicURL + LoginPath + login.ID,
			ElicitationID: login.ID,
		}},
		RequestState: logins.sign(login.ID, session),
	}
}

// resumeLogin handles the retry that follows a login elicitation: it waits for the browser flow to finish and
// reconnects the server. A nil result means the call can proceed.
func (r *Router) resumeLogin(ctx context.Context, req *mcp.CallToolRequest, server string) *mcp.CallToolResult {
	logins := r.loginSupport(req)
	if logins == nil || req.Params.RequestState == "" {
		return nil
	}

	unauthorized := &toolError{Code: codeUnauthorized, Server: server, Message: server + " requires authorization", Hint: loginHint(server)}

	session := sessionID(req)

	id, ok := logins.verify(req.Params.RequestState, session)
	if !ok {
		unauthorized.Message = "the sign-in link does not belong to this session"

		return errorResult(unauthorized)
	}

	if response, isElicit := req.Params.InputResponses[loginInput].(*mcp.ElicitResult); !isElicit || response.Action != actionAccept {
		unauthorized.Message = "the user did not sign in to " + server

		return errorResult(unauthorized)
	}

	login := logins.take(id)
	if login == nil {
		unauthorized.Message = "the sign-in link expired"

		return errorResult(unauthorized)
	}

	select {
	case <-login.Done():
	case <-ctx.Done():
		unauthorized.Message = "the call was cancelled while waiting for sign-in"

		return errorResult(unauthorized)
	}

	if err := login.Err(); err != nil {
		unauthorized.Message = "sign-in failed: " + err.Error()

		return errorResult(unauthorized)
	}

	if req.Session != nil {
		_ = req.Session.NotifyElicitationComplete(ctx, &mcp.ElicitationCompleteParams{ElicitationID: login.ID})
	}

	if err := logins.options.Reconnect(ctx, server); err != nil {
		r.logger.WarnContext(ctx, "reconnect after login failed", slog.String("server", server), slog.String("error", err.Error()))
	}

	return nil
}

// loginSupport returns the login configuration when logins are enabled and the client can open URLs for the user.
func (r *Router) loginSupport(req *mcp.CallToolRequest) *elicitedLogins {
	r.mu.Lock()
	logins := r.logins
	r.mu.Unlock()

	if logins == nil || req.Session == nil {
		return nil
	}

	capabilities := req.ClientCapabilities()
	if capabilities == nil || capabilities.Elicitation == nil || capabilities.Elicitation.URL == nil {
		return nil
	}

	return logins
}

func (l *elicitedLogins) subject(session string) string {
	if l.options.Subject == nil {
		return ""
	}

	return l.options.Subject(session)
}

func (l *elicitedLogins) remember(login *oauth.Login) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for id, active := range l.active {
		if now.After(active.ExpiresAt) {
			delete(l.active, id)
		}
	}

	l.active[login.ID] = login
}

func (l *elicitedLogins) take(id string) *oauth.Login {
	l.mu.Lock()
	defer l.mu.Unlock()

	login := l.active[id]
	delete(l.active, id)

	return login
}

// sign binds a login to the MCP session that requested it, so the request state cannot be replayed elsewhere.
func (l *elicitedLogins) sign(id, session string) string {
	return id + "." + l.mac(id, session)
}

func (l *elicitedLogins) verify(state, session string) (string, bool) {
	id, mac, ok := strings.Cut(state, ".")
	if !ok {
		return "", false
	}

	return id, hmac.Equal([]byte(mac), []byte(l.mac(id, session)))
}

func (l *elicitedLogins) mac(id, session string) string {
	digest := hmac.New(sha256.New, l.key)
	digest.Write([]byte(id + "\x00" + session))

	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}
