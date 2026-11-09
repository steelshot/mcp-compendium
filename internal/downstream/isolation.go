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

package downstream

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// isolatedConns holds the per-client-session connections of one server with isolation: session. The server's shared
// connection still provides discovery and the catalogue; tool calls from each client session go to its own connection,
// so stateful servers such as browsers never share state between agents.
type isolatedConns struct {
	base *conn

	mu       sync.Mutex
	sessions map[string]*sessionConn
}

// sessionConn is one client session's connection and when it was last used.
type sessionConn struct {
	conn     *conn
	lastUsed time.Time
}

const (
	// maxSessionsPerServer caps the processes or connections one isolated server may hold.
	maxSessionsPerServer = 32
	// sessionIdleTimeout closes a session's connection after this long without a call.
	sessionIdleTimeout = 30 * time.Minute
)

func newIsolatedConns(base *conn) *isolatedConns {
	return &isolatedConns{base: base, sessions: make(map[string]*sessionConn)}
}

// call routes a tool call to the session's own connection, creating it on first use.
func (i *isolatedConns) call(ctx context.Context, session string, req *CallRequest) (*mcp.CallToolResult, error) {
	sc, err := i.acquire(session)
	if err != nil {
		return nil, err
	}

	return sc.call(ctx, req.Tool, req.Arguments, req.Retryable, req.Progress)
}

func (i *isolatedConns) acquire(session string) (*conn, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if existing, ok := i.sessions[session]; ok {
		existing.lastUsed = i.base.now()

		return existing.conn, nil
	}

	if len(i.sessions) >= maxSessionsPerServer {
		return nil, &Error{
			Kind: KindUnavailable, Server: i.base.cfg.Name, RetryAfter: time.Minute,
			Err: fmt.Errorf("%w: %d concurrent sessions", errTooManySessions, maxSessionsPerServer),
		}
	}

	// Per-session connections report nothing to the catalogue; the shared connection represents the server there.
	created := newConn(i.base.cfg, i.base.settings, i.base.authorizer, i.base.logger.With(slog.String("session", session)), func() {})
	i.sessions[session] = &sessionConn{conn: created, lastUsed: i.base.now()}

	return created, nil
}

// close disconnects the session's connection, if any.
func (i *isolatedConns) close(session string) {
	i.mu.Lock()
	sc, ok := i.sessions[session]
	delete(i.sessions, session)
	i.mu.Unlock()

	if ok {
		sc.conn.close()
	}
}

// reap closes connections idle for longer than sessionIdleTimeout.
func (i *isolatedConns) reap() {
	cutoff := i.base.now().Add(-sessionIdleTimeout)

	i.mu.Lock()

	idle := make([]*conn, 0)

	for session, sc := range i.sessions {
		if sc.lastUsed.Before(cutoff) {
			idle = append(idle, sc.conn)
			delete(i.sessions, session)
		}
	}

	i.mu.Unlock()

	for _, c := range idle {
		c.close()
	}
}

func (i *isolatedConns) closeAll() {
	i.mu.Lock()
	sessions := i.sessions
	i.sessions = make(map[string]*sessionConn)
	i.mu.Unlock()

	for _, sc := range sessions {
		sc.conn.close()
	}
}
