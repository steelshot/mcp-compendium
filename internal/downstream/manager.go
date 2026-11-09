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

// Package downstream manages connections to the configured MCP servers: parallel discovery at startup, cached tool
// lists, cooldown-limited reconnects, background recovery and tool calls with timeouts and progress forwarding.
package downstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// Status is the connection state of a downstream server.
type Status string

// Snapshot is an immutable view of one server, used to build the agent-facing catalogue.
type Snapshot struct {
	Name        string
	Description string
	Status      Status
	LastError   string
	RetryAt     time.Time
	// Tools is every tool the server advertises, before configured filters are applied.
	Tools  []*mcp.Tool
	Config *config.Server
}

// CallRequest describes one tool call.
type CallRequest struct {
	Server    string
	Tool      string
	Arguments any
	// Retryable allows a retry after a transport failure mid-call; set it only for read-only or idempotent tools.
	Retryable bool
	Progress  ProgressFunc
	// Session identifies the calling client session; servers with isolation: session give each one a connection.
	Session string
}

// Manager owns the connections to every enabled server.
type Manager struct {
	conns    map[string]*conn
	order    []*conn
	isolated map[string]*isolatedConns
	logger   *slog.Logger
	ready    chan struct{}

	mu        sync.Mutex
	listeners []func()
}

// Connection states.
const (
	StatusConnecting   Status = "connecting"
	StatusOK           Status = "ok"
	StatusUnauthorized Status = "unauthorized"
	StatusUnavailable  Status = "unavailable"

	recoveryInterval = time.Second
)

var errStillConnecting = errors.New("still connecting to downstream servers")

// New prepares connections to the enabled servers without connecting.
func New(servers []*config.Server, settings Settings, authorizer Authorizer, logger *slog.Logger) *Manager {
	manager := &Manager{
		conns: make(map[string]*conn, len(servers)), isolated: make(map[string]*isolatedConns), logger: logger, ready: make(chan struct{}),
	}

	for _, server := range servers {
		if !server.Enabled {
			continue
		}

		c := newConn(server, settings, authorizer, logger, manager.changed)
		manager.conns[server.Name] = c
		manager.order = append(manager.order, c)

		if server.Isolation == config.IsolationSession {
			manager.isolated[server.Name] = newIsolatedConns(c)
		}
	}

	return manager
}

// OnChange registers fn to run whenever any server's status or tools change.
func (m *Manager) OnChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.listeners = append(m.listeners, fn)
}

// Start connects to every server in parallel, then keeps reconnecting failed servers in the background until ctx is
// cancelled. It returns immediately; Ready reports when the initial attempts have finished.
func (m *Manager) Start(ctx context.Context) {
	var group sync.WaitGroup

	for _, c := range m.order {
		group.Go(func() { _ = c.connect(ctx) })
	}

	go func() {
		group.Wait()
		close(m.ready)
		m.recover(ctx)
	}()
}

// Ready returns nil once every server has been tried at least once.
func (m *Manager) Ready() error {
	select {
	case <-m.ready:
		return nil
	default:
		return errStillConnecting
	}
}

// WaitReady blocks until Ready returns nil or ctx is done.
func (m *Manager) WaitReady(ctx context.Context) error {
	select {
	case <-m.ready:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for downstream servers: %w", context.Cause(ctx))
	}
}

// Snapshots returns every enabled server's state in name order.
func (m *Manager) Snapshots() []Snapshot {
	snapshots := make([]Snapshot, 0, len(m.order))
	for _, c := range m.order {
		snapshots = append(snapshots, c.snapshot())
	}

	return snapshots
}

// Call invokes a tool on a downstream server.
func (m *Manager) Call(ctx context.Context, req *CallRequest) (*mcp.CallToolResult, error) {
	c, ok := m.conns[req.Server]
	if !ok {
		return nil, &Error{Kind: KindUnknownServer, Server: req.Server, Err: errUnknownServer}
	}

	if isolated, ok := m.isolated[req.Server]; ok && req.Session != "" {
		return isolated.call(ctx, req.Session, req)
	}

	return c.call(ctx, req.Tool, req.Arguments, req.Retryable, req.Progress)
}

// CloseSession disconnects every per-session connection belonging to a client session that has ended.
func (m *Manager) CloseSession(session string) {
	for _, isolated := range m.isolated {
		isolated.close(session)
	}
}

// Reconnect forces a new connection to server, ignoring any cooldown.
func (m *Manager) Reconnect(ctx context.Context, server string) error {
	c, ok := m.conns[server]
	if !ok {
		return &Error{Kind: KindUnknownServer, Server: server, Err: errUnknownServer}
	}

	return c.connect(ctx)
}

// Close disconnects every server, stopping stdio processes.
func (m *Manager) Close() {
	var group sync.WaitGroup

	for _, c := range m.order {
		group.Go(c.close)
	}

	for _, isolated := range m.isolated {
		group.Go(isolated.closeAll)
	}

	group.Wait()
}

func (m *Manager) recover(ctx context.Context) {
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, c := range m.order {
				if c.retryDue() {
					go func() { _ = c.connect(ctx) }()
				}
			}

			for _, isolated := range m.isolated {
				isolated.reap()
			}
		}
	}
}

func (m *Manager) changed() {
	m.mu.Lock()
	listeners := append([]func(){}, m.listeners...)
	m.mu.Unlock()

	for _, listener := range listeners {
		listener()
	}
}
