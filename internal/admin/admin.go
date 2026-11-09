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

package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/admin/gen"
	"github.com/steelshot/mcp-compendium/internal/app"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/inbound"
	"github.com/steelshot/mcp-compendium/internal/oauth"
	"github.com/steelshot/mcp-compendium/internal/router"
)

// Pool is the part of the downstream pool the admin API uses.
type Pool interface {
	Snapshots() []downstream.Snapshot
	Reconnect(ctx context.Context, server string) error
}

// Logins is the part of the OAuth manager the admin API uses.
type Logins interface {
	StartLogin(ctx context.Context, server, redirectURL, subject string) (*oauth.Login, error)
	Complete(ctx context.Context, id string, redirect url.Values) error
	Logout(server string) (bool, error)
	HasCredential(server string) bool
}

// Handler implements the generated admin API.
type Handler struct {
	pool   Pool
	logins Logins
	logger *slog.Logger
}

// problemError carries an HTTP status for the error handler.
type problemError struct {
	status int
	title  string
	err    error
}

// security accepts every request: the inbound middleware in front of the API has already authenticated the caller
// and checked the admin role.
type security struct{}

// Path is where the API is mounted.
const Path = "/admin/v1"

var (
	errUnknownServer   = errors.New("unknown server")
	errInvalidRedirect = errors.New("not an absolute http(s) URL")
)

// New returns the admin API as an http.Handler mounted at Path.
func New(pool Pool, logins Logins, logger *slog.Logger) (http.Handler, error) {
	server, err := gen.NewServer(&Handler{pool: pool, logins: logins, logger: logger}, security{}, gen.WithPathPrefix(Path))
	if err != nil {
		return nil, fmt.Errorf("build admin API: %w", err)
	}

	return server, nil
}

// ListServers implements listServers.
func (h *Handler) ListServers(context.Context) (*gen.ServerList, error) {
	snapshots := h.pool.Snapshots()
	list := &gen.ServerList{Version: app.Version(), Servers: make([]gen.ServerStatus, 0, len(snapshots))}

	for index := range snapshots {
		list.Servers = append(list.Servers, h.status(&snapshots[index]))
	}

	return list, nil
}

// GetServer implements getServer.
func (h *Handler) GetServer(_ context.Context, params gen.GetServerParams) (*gen.ServerDetail, error) {
	snapshot, err := h.snapshot(params.Server)
	if err != nil {
		return nil, err
	}

	exposure := router.Partition(snapshot)
	detail := &gen.ServerDetail{Server: h.status(snapshot), Tools: make([]gen.Tool, 0, len(exposure.Exposed)+len(exposure.Filtered))}

	for _, tool := range exposure.Exposed {
		detail.Tools = append(detail.Tools, describeTool(tool, true))
	}

	for _, tool := range exposure.Filtered {
		detail.Tools = append(detail.Tools, describeTool(tool, false))
	}

	return detail, nil
}

// ReconnectServer implements reconnectServer. A failed reconnect is reported through the returned status.
func (h *Handler) ReconnectServer(ctx context.Context, params gen.ReconnectServerParams) (*gen.ServerStatus, error) {
	if _, err := h.snapshot(params.Server); err != nil {
		return nil, err
	}

	if err := h.pool.Reconnect(ctx, params.Server); err != nil {
		h.logger.InfoContext(ctx, "reconnect failed", slog.String("server", params.Server), slog.String("error", err.Error()))
	}

	snapshot, err := h.snapshot(params.Server)
	if err != nil {
		return nil, err
	}

	status := h.status(snapshot)

	return &status, nil
}

// StartLogin implements startLogin.
func (h *Handler) StartLogin(ctx context.Context, req *gen.LoginRequest, params gen.StartLoginParams) (*gen.Login, error) {
	redirect, err := url.Parse(req.RedirectUri)
	if err != nil || (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Host == "" {
		return nil, &problemError{
			status: http.StatusBadRequest,
			title:  "invalid redirectUri",
			err:    fmt.Errorf("%w: %q", errInvalidRedirect, req.RedirectUri),
		}
	}

	subject := ""
	if principal, ok := inbound.PrincipalFromContext(ctx); ok {
		subject = principal.Subject
	}

	login, err := h.logins.StartLogin(ctx, params.Server, req.RedirectUri, subject)
	if err != nil {
		return nil, err
	}

	h.logger.InfoContext(ctx, "login started", slog.String("server", params.Server), slog.String("subject", subject))

	return &gen.Login{ID: login.ID, Server: login.Server, AuthorizationUrl: login.AuthorizationURL, ExpiresAt: login.ExpiresAt}, nil
}

// CompleteLogin implements completeLogin.
func (h *Handler) CompleteLogin(ctx context.Context, req *gen.LoginCallback, params gen.CompleteLoginParams) error {
	redirect := url.Values{"state": {req.State}}
	for key, value := range map[string]gen.OptString{"code": req.Code, "iss": req.Iss, "error": req.Error, "error_description": req.ErrorDescription} {
		if text, ok := value.Get(); ok {
			redirect.Set(key, text)
		}
	}

	return h.logins.Complete(ctx, params.Login, redirect)
}

// Logout implements logout.
func (h *Handler) Logout(ctx context.Context, params gen.LogoutParams) (*gen.LogoutResult, error) {
	deleted, err := h.logins.Logout(params.Server)
	if err != nil {
		return nil, err
	}

	h.logger.InfoContext(ctx, "logout", slog.String("server", params.Server), slog.Bool("deleted", deleted))

	return &gen.LogoutResult{Deleted: deleted}, nil
}

// NewError maps errors to RFC 9457 problem documents.
func (h *Handler) NewError(ctx context.Context, err error) *gen.ProblemStatusCode {
	status, title := http.StatusInternalServerError, "internal error"

	var problem *problemError

	switch {
	case errors.As(err, &problem):
		status, title = problem.status, problem.title
	case errors.Is(err, errUnknownServer), errors.Is(err, oauth.ErrLoginNotFound):
		status, title = http.StatusNotFound, "not found"
	case errors.Is(err, oauth.ErrNotRequired):
		status, title = http.StatusConflict, "login not required"
	case errors.Is(err, oauth.ErrStateMismatch), errors.Is(err, oauth.ErrDenied):
		status, title = http.StatusBadRequest, "login rejected"
	case errors.Is(err, context.DeadlineExceeded):
		status, title = http.StatusGatewayTimeout, "timed out"
	}

	if status >= http.StatusInternalServerError {
		h.logger.ErrorContext(ctx, "admin request failed", slog.String("error", err.Error()))
	}

	return &gen.ProblemStatusCode{
		StatusCode: status,
		Response:   gen.Problem{Title: title, Status: status, Detail: gen.NewOptString(err.Error())},
	}
}

func (e *problemError) Error() string {
	return e.err.Error()
}

func (e *problemError) Unwrap() error {
	return e.err
}

// HandleBasic implements gen.SecurityHandler.
func (security) HandleBasic(ctx context.Context, _ gen.OperationName, _ gen.Basic) (context.Context, error) {
	return ctx, nil
}

// HandleBearer implements gen.SecurityHandler.
func (security) HandleBearer(ctx context.Context, _ gen.OperationName, _ gen.Bearer) (context.Context, error) {
	return ctx, nil
}

func describeTool(tool *mcp.Tool, exposed bool) gen.Tool {
	described := gen.Tool{Name: tool.Name, Exposed: exposed, Hints: router.Hints(tool)}
	if tool.Description != "" {
		described.Description = gen.NewOptString(tool.Description)
	}

	return described
}

func (h *Handler) snapshot(server string) (*downstream.Snapshot, error) {
	for _, snapshot := range h.pool.Snapshots() {
		if snapshot.Name == server {
			return &snapshot, nil
		}
	}

	return nil, fmt.Errorf("%w %q", errUnknownServer, server)
}

func (h *Handler) status(snapshot *downstream.Snapshot) gen.ServerStatus {
	exposure := router.Partition(snapshot)
	status := gen.ServerStatus{
		Name:          snapshot.Name,
		Transport:     gen.ServerStatusTransport(snapshot.Config.Transport),
		Status:        gen.ServerStatusStatus(snapshot.Status),
		Tools:         len(exposure.Exposed),
		Filtered:      len(exposure.Filtered),
		Authenticated: snapshot.Config.URL != nil && h.logins.HasCredential(snapshot.Name),
	}

	if snapshot.Description != "" {
		status.Description = gen.NewOptString(snapshot.Description)
	}

	if snapshot.LastError != "" {
		status.LastError = gen.NewOptString(snapshot.LastError)
	}

	if !snapshot.RetryAt.IsZero() && snapshot.Status != downstream.StatusOK {
		status.RetryAt = gen.NewOptDateTime(snapshot.RetryAt)
	}

	return status
}
