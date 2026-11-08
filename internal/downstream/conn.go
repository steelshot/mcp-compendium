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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"

	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/config"
)

// ProgressFunc receives progress notifications for one tool call.
type ProgressFunc func(ctx context.Context, params *mcp.ProgressNotificationParams)

// conn owns the connection to one downstream server and its cached tool list.
type conn struct {
	cfg        *config.Server
	settings   Settings
	authorizer Authorizer
	logger     *slog.Logger
	notify     func()
	now        func() time.Time

	connects   singleflight.Group
	challenged atomic.Bool
	progress   sync.Map
	tokens     atomic.Uint64

	mu       sync.Mutex
	session  *mcp.ClientSession
	status   Status
	lastErr  error
	tools    []*mcp.Tool
	init     *mcp.InitializeResult
	failures int
	retryAt  time.Time
	closed   bool
}

const (
	minBackoff  = 2 * time.Second
	maxBackoff  = time.Minute
	listTimeout = 30 * time.Second
)

func newConn(server *config.Server, settings Settings, authorizer Authorizer, logger *slog.Logger, notify func()) *conn {
	if server.Limits.StartupTimeout > 0 {
		settings.StartupTimeout = server.Limits.StartupTimeout
	}

	if server.Limits.ExecutionTimeout > 0 {
		settings.ExecutionTimeout = server.Limits.ExecutionTimeout
	}

	return &conn{
		cfg:        server,
		settings:   settings,
		authorizer: authorizer,
		logger:     logger.With(slog.String("server", server.Name)),
		notify:     notify,
		now:        time.Now,
		status:     StatusConnecting,
	}
}

func listTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	if result := session.InitializeResult(); result != nil && result.Capabilities != nil && result.Capabilities.Tools == nil {
		return nil, nil
	}

	tools := make([]*mcp.Tool, 0)

	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// describeServer prefers the configured description, then the server's own title and first sentence of instructions.
func describeServer(server *config.Server, init *mcp.InitializeResult) string {
	if server.Description != "" || init == nil {
		return server.Description
	}

	parts := make([]string, 0, 2)
	if init.ServerInfo != nil && init.ServerInfo.Title != "" {
		parts = append(parts, strings.TrimSuffix(init.ServerInfo.Title, "."))
	}

	if sentence := firstSentence(init.Instructions); sentence != "" {
		parts = append(parts, sentence)
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, ". ")
}

func firstSentence(text string) string {
	const maxLength = 200

	text = strings.Join(strings.Fields(text), " ")
	if index := strings.Index(text, ". "); index >= 0 {
		text = text[:index+1]
	}

	if len(text) > maxLength {
		text = strings.TrimSpace(text[:maxLength]) + "…"
	}

	return text
}

func backoff(failures int) time.Duration {
	delay := minBackoff
	for range failures - 1 {
		delay *= 2
		if delay >= maxBackoff {
			return maxBackoff
		}
	}

	return delay
}

func orClosed(err error) error {
	if err == nil {
		return mcp.ErrConnectionClosed
	}

	return err
}

func orNotConnected(err error) error {
	if err == nil {
		return errNotConnected
	}

	return err
}

// snapshot returns the current state.
func (c *conn) snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := Snapshot{
		Name:        c.cfg.Name,
		Description: describeServer(c.cfg, c.init),
		Status:      c.status,
		RetryAt:     c.retryAt,
		Tools:       c.tools,
		Config:      c.cfg,
	}
	if c.lastErr != nil {
		snapshot.LastError = c.lastErr.Error()
	}

	return snapshot
}

// connect (re)establishes the connection. Concurrent callers share one attempt, which is detached from any caller's
// cancellation and bounded by the startup timeout.
func (c *conn) connect(ctx context.Context) error {
	_, err, _ := c.connects.Do("connect", func() (any, error) {
		return nil, c.doConnect(context.WithoutCancel(ctx))
	})

	return err //nolint:wrapcheck // doConnect already returns classified *Error values
}

func (c *conn) doConnect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.settings.StartupTimeout)
	defer cancel()

	c.closeSession()
	c.challenged.Store(false)

	client := mcp.NewClient(&mcp.Implementation{Name: app.Name, Version: app.Version()}, &mcp.ClientOptions{
		// No roots, sampling or elicitation: downstream servers must not reach the agent through the daemon.
		Capabilities: &mcp.ClientCapabilities{},
		ToolListChangedHandler: func(ctx context.Context, _ *mcp.ToolListChangedRequest) {
			go c.refreshTools(context.WithoutCancel(ctx))
		},
		ProgressNotificationHandler: c.onProgress,
	})

	start := c.now()

	session, err := client.Connect(ctx, buildTransport(ctx, c.cfg, c.authorizer, &c.challenged, c.logger), nil)
	if err != nil {
		return c.fail(ctx, fmt.Errorf("connect: %w", err))
	}

	tools, err := listTools(ctx, session)
	if err != nil {
		_ = session.Close()

		return c.fail(ctx, fmt.Errorf("list tools: %w", err))
	}

	c.mu.Lock()
	c.session = session
	c.status = StatusOK
	c.tools = tools
	c.init = session.InitializeResult()
	c.lastErr = nil
	c.failures = 0
	c.retryAt = time.Time{}
	c.mu.Unlock()

	c.logger.InfoContext(ctx, "connected", slog.Int("tools", len(tools)), slog.Duration("elapsed", c.now().Sub(start)))

	go c.watch(session)

	c.notify()

	return nil
}

// fail records a failed connection attempt and schedules the next one.
func (c *conn) fail(ctx context.Context, err error) error {
	kind, status := KindUnavailable, StatusUnavailable

	switch {
	case c.challenged.Load():
		kind, status, err = KindUnauthorized, StatusUnauthorized, errUnauthorized
	case errors.Is(err, context.DeadlineExceeded):
		err = fmt.Errorf("%w (%s)", errStartupTimeout, c.settings.StartupTimeout)
	}

	c.mu.Lock()
	c.status = status
	c.lastErr = err
	c.failures++
	c.retryAt = c.now().Add(backoff(c.failures))
	retryAfter := c.retryAt.Sub(c.now())
	c.mu.Unlock()

	c.logger.WarnContext(ctx, "connection failed", slog.String("status", string(status)), slog.String("error", err.Error()))
	c.notify()

	return &Error{Kind: kind, Server: c.cfg.Name, RetryAfter: retryAfter, Err: err}
}

// watch marks the connection unavailable when the session ends without being closed by the daemon.
func (c *conn) watch(session *mcp.ClientSession) {
	err := session.Wait()

	c.mu.Lock()
	if c.session != session || c.closed {
		c.mu.Unlock()

		return
	}

	c.session = nil
	c.status = StatusUnavailable
	c.lastErr = fmt.Errorf("connection closed: %w", orClosed(err))
	c.retryAt = c.now()
	c.mu.Unlock()

	c.logger.Warn("connection closed", slog.String("error", c.lastErr.Error()))
	c.notify()
}

func (c *conn) refreshTools(ctx context.Context) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	tools, err := listTools(ctx, session)
	if err != nil {
		c.logger.WarnContext(ctx, "refresh tools failed", slog.String("error", err.Error()))

		return
	}

	c.mu.Lock()
	if c.session == session {
		c.tools = tools
	}
	c.mu.Unlock()

	c.notify()
}

// ready returns a live session, reconnecting when the server is down and its cooldown has elapsed.
func (c *conn) ready(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	session, status, retryAt, lastErr := c.session, c.status, c.retryAt, c.lastErr
	c.mu.Unlock()

	if session != nil && status == StatusOK {
		return session, nil
	}

	if wait := retryAt.Sub(c.now()); status != StatusConnecting && wait > 0 {
		kind := KindUnavailable
		if status == StatusUnauthorized {
			kind = KindUnauthorized
		}

		return nil, &Error{Kind: kind, Server: c.cfg.Name, RetryAfter: wait, Err: orNotConnected(lastErr)}
	}

	if err := c.connect(ctx); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.session == nil {
		return nil, &Error{Kind: KindUnavailable, Server: c.cfg.Name, Err: errNotConnected}
	}

	return c.session, nil
}

// call invokes a tool. A transport failure before the call was sent triggers one reconnect and retry; a failure after
// it was sent is retried only when retryable, because a non-idempotent tool may already have acted.
func (c *conn) call(ctx context.Context, tool string, arguments any, retryable bool, progress ProgressFunc) (*mcp.CallToolResult, error) {
	session, err := c.ready(ctx)
	if err != nil {
		return nil, err
	}

	result, err := c.invoke(ctx, session, tool, arguments, progress)
	if err == nil || ctx.Err() != nil {
		return result, err
	}

	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindUnavailable {
		return nil, err
	}

	if !retryable {
		classified.MayHaveRun = true

		return nil, classified
	}

	c.logger.WarnContext(ctx, "retrying after transport failure", slog.String("tool", tool), slog.String("error", err.Error()))

	if reconnectErr := c.connect(ctx); reconnectErr != nil {
		return nil, reconnectErr
	}

	session, err = c.ready(ctx)
	if err != nil {
		return nil, err
	}

	return c.invoke(ctx, session, tool, arguments, progress)
}

func (c *conn) invoke(ctx context.Context, session *mcp.ClientSession, tool string, arguments any, progress ProgressFunc) (*mcp.CallToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.settings.ExecutionTimeout)
	defer cancel()

	params := &mcp.CallToolParams{Name: tool, Arguments: arguments}

	if progress != nil {
		token := c.cfg.Name + "-" + strconv.FormatUint(c.tokens.Add(1), 10)
		params.SetProgressToken(token)
		c.progress.Store(token, progress)

		defer c.progress.Delete(token)
	}

	result, err := session.CallTool(callCtx, params)
	if err == nil {
		return result, nil
	}

	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("call %s: %w", tool, ctx.Err())
	case callCtx.Err() != nil:
		return nil, &Error{
			Kind: KindTimeout, Server: c.cfg.Name, MayHaveRun: true,
			Err: fmt.Errorf("%w: %s did not finish within %s", errExecutionTimeout, tool, c.settings.ExecutionTimeout),
		}
	case isProtocolError(err):
		return nil, fmt.Errorf("call %s: %w", tool, err)
	case c.challenged.Load():
		c.markDown(session, StatusUnauthorized, errUnauthorized)

		return nil, &Error{Kind: KindUnauthorized, Server: c.cfg.Name, Err: errUnauthorized}
	default:
		c.markDown(session, StatusUnavailable, err)

		return nil, &Error{Kind: KindUnavailable, Server: c.cfg.Name, Err: err}
	}
}

// markDown closes a failed session so the next call reconnects.
func (c *conn) markDown(session *mcp.ClientSession, status Status, err error) {
	c.mu.Lock()
	if c.session != session {
		c.mu.Unlock()

		return
	}

	c.session = nil
	c.status = status
	c.lastErr = err
	c.retryAt = c.now()
	c.mu.Unlock()

	go func() { _ = session.Close() }()

	c.notify()
}

func (c *conn) onProgress(ctx context.Context, req *mcp.ProgressNotificationClientRequest) {
	token, ok := req.Params.ProgressToken.(string)
	if !ok {
		return
	}

	if handler, found := c.progress.Load(token); found {
		if forward, typed := handler.(ProgressFunc); typed {
			forward(ctx, req.Params)
		}
	}
}

// retryDue reports whether a failed, non-unauthorized server is due for a background reconnect.
func (c *conn) retryDue() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return !c.closed && c.status == StatusUnavailable && !c.now().Before(c.retryAt)
}

func (c *conn) closeSession() {
	c.mu.Lock()
	session := c.session
	c.session = nil
	c.mu.Unlock()

	if session != nil {
		_ = session.Close()
	}
}

func (c *conn) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	c.closeSession()
}
