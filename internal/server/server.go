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

// Package server wires the HTTP listener: the MCP endpoint, health probes and graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Readiness reports nil when the daemon can serve traffic, or the reason it cannot.
type Readiness func() error

// Server is the daemon's HTTP listener.
type Server struct {
	settings Settings
	logger   *slog.Logger
	mux      *http.ServeMux
	ready    atomic.Pointer[Readiness]
}

const readHeaderTimeout = 10 * time.Second

// New returns a server with the health probes registered. Readiness defaults to ready.
func New(settings Settings, logger *slog.Logger) *Server {
	s := &Server{settings: settings, logger: logger, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /livez", s.handleLive)
	s.mux.HandleFunc("GET /readyz", s.handleReady)

	return s
}

// Handle registers a handler on the server's mux using http.ServeMux pattern syntax.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// SetReadiness replaces the readiness check used by /readyz.
func (s *Server) SetReadiness(check Readiness) {
	s.ready.Store(&check)
}

// Handler returns the root handler, for tests and embedding.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Listen opens the configured listen address.
func (s *Server) Listen(ctx context.Context) (net.Listener, error) {
	var config net.ListenConfig

	listener, err := config.Listen(ctx, "tcp", s.settings.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", s.settings.Listen, err)
	}

	return listener, nil
}

// Serve serves on listener until ctx is cancelled, then drains in-flight requests for up to the shutdown timeout.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	httpServer := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}

	served := make(chan error, 1)

	go func() {
		served <- httpServer.Serve(listener)
	}()

	s.logger.InfoContext(ctx, "listening", slog.String("address", listener.Addr().String()))

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	s.logger.InfoContext(ctx, "shutting down", slog.Duration("timeout", s.settings.ShutdownTimeout))

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.settings.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// Run listens and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	listener, err := s.Listen(ctx)
	if err != nil {
		return err
	}

	return s.Serve(ctx, listener)
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

func (*Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	writePlain(w, http.StatusOK, "ok")
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	check := s.ready.Load()
	if check == nil {
		writePlain(w, http.StatusOK, "ok")

		return
	}

	if err := (*check)(); err != nil {
		writePlain(w, http.StatusServiceUnavailable, err.Error())

		return
	}

	writePlain(w, http.StatusOK, "ok")
}
