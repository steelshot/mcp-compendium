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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/config"
)

// Authorizer attaches credentials to requests sent to OAuth-protected servers. The OAuth package implements it.
type Authorizer interface {
	// RoundTripper wraps next for requests to server.
	RoundTripper(server *config.Server, next http.RoundTripper) http.RoundTripper
}

// headerTransport sets the configured static headers on every request.
type headerTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

// challengeTransport records 401 responses so connection failures can be classified as needing authorization.
type challengeTransport struct {
	challenged *atomic.Bool
	next       http.RoundTripper
}

// lineLogger logs a child process's stderr line by line.
type lineLogger struct {
	ctx    context.Context //nolint:containedctx // the logger outlives any single call and logs with the server's base context
	logger *slog.Logger
	mu     sync.Mutex
	buf    bytes.Buffer
}

const (
	maxStderrLine = 4 << 10

	// JSON-RPC codes the go-sdk uses for local connection failures (internal/jsonrpc2/wire.go).
	codeClientClosing       = -32003
	codeServerClosing       = -32004
	codeRejectedByTransport = -32005
)

// inheritedEnv lists the daemon environment variables passed to stdio servers. Everything else, notably the daemon's
// own secrets, must be passed explicitly through the server's env map.
//
//nolint:gochecknoglobals // fixed allow-list
var inheritedEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "LANGUAGE": true, "TZ": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
	"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "NODE_USE_SYSTEM_CA": true,
	"SYSTEMROOT": true, "COMSPEC": true, "PATHEXT": true, "WINDIR": true, "APPDATA": true, "LOCALAPPDATA": true,
	"USERPROFILE": true, "PROGRAMDATA": true, "PROGRAMFILES": true,
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		req = req.Clone(req.Context())
		for name, value := range t.headers {
			req.Header.Set(name, value)
		}
	}

	return t.next.RoundTrip(req) //nolint:wrapcheck // transports must return the underlying error unchanged
}

func (t challengeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		t.challenged.Store(true)
	}

	return resp, err //nolint:wrapcheck // transports must return the underlying error unchanged
}

func (l *lineLogger) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf.Write(data)

	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// Keep the partial line for the next write, unless it is already oversized.
			if len(line) > maxStderrLine {
				l.emit(line)
			} else {
				l.buf.WriteString(line)
			}

			return len(data), nil
		}

		l.emit(line)
	}
}

// buildTransport creates a fresh transport for server. Stdio transports start a new process on every connect.
func buildTransport(ctx context.Context, server *config.Server, authorizer Authorizer, challenged *atomic.Bool, logger *slog.Logger) mcp.Transport {
	if server.Transport == config.TransportStdio {
		//nolint:gosec,noctx // the command comes from the operator's configuration; the SDK owns the process lifetime
		cmd := exec.Command(server.Command, server.Args...)
		cmd.Env = childEnv(os.Environ(), server.Env)
		cmd.Dir = server.Dir
		cmd.Stderr = &lineLogger{ctx: context.WithoutCancel(ctx), logger: logger}

		return &mcp.CommandTransport{Command: cmd}
	}

	next := baseTransport()
	if authorizer != nil {
		next = authorizer.RoundTripper(server, next)
	}

	client := &http.Client{
		Transport: challengeTransport{challenged: challenged, next: headerTransport{headers: server.Headers, next: next}},
	}

	if server.Transport == config.TransportSSE {
		return &mcp.SSEClientTransport{Endpoint: server.URL.String(), HTTPClient: client}
	}

	return &mcp.StreamableClientTransport{Endpoint: server.URL.String(), HTTPClient: client}
}

func baseTransport() http.RoundTripper {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}

	return http.DefaultTransport
}

// childEnv returns the allow-listed subset of environ plus the server's explicit variables, which take precedence.
func childEnv(environ []string, extra map[string]string) []string {
	result := make([]string, 0, len(extra)+len(inheritedEnv))

	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(name)
		}

		if _, overridden := extra[name]; overridden || !inheritedEnv[key] {
			continue
		}

		result = append(result, entry)
	}

	for name, value := range extra {
		result = append(result, name+"="+value)
	}

	return result
}

// isProtocolError reports whether err is a JSON-RPC error returned by the server, as opposed to a transport failure.
// The SDK reports local connection failures as wire errors too, using its reserved codes, which are excluded here.
func isProtocolError(err error) bool {
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) {
		return false
	}

	switch wire.Code {
	case codeClientClosing, codeServerClosing, codeRejectedByTransport:
		return false
	default:
		return true
	}
}

func (l *lineLogger) emit(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}

	if len(line) > maxStderrLine {
		line = line[:maxStderrLine] + "…"
	}

	l.logger.InfoContext(l.ctx, "stderr", slog.String("line", line))
}
