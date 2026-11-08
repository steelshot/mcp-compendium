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
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/testmcp"
)

func TestMain(m *testing.M) {
	if code, ok := testmcp.RunStdioIfRequested(func() *mcp.Server {
		return testmcp.NewServer(
			"stdio-fake",
			"",
			"",
			testmcp.Tool{Name: "echo", Description: "Echo arguments."},
			testmcp.Tool{Name: "crash", Description: "Exit the process.", Handler: testmcp.Crash},
			testmcp.Tool{
				Name:        "pid",
				Description: "Report the process ID.",
				Handler: func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, error) {
					return testmcp.Text(strconv.Itoa(os.Getpid())), nil
				},
			},
		)
	}); ok {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestHTTPConnectCallAndProgress(t *testing.T) {
	t.Parallel()

	endpoint := testmcp.ServeHTTP(t, testmcp.NewServer("fake", "Fake Server", "Use it for testing. It does nothing else.",
		testmcp.Tool{Name: "echo", Description: "Echo arguments.", Properties: map[string]string{"text": "string"}},
		testmcp.Tool{Name: "steps", Description: "Report progress.", Handler: testmcp.Progress(3)},
	))

	manager := startManager(t, httpServer("fake", endpoint))
	snapshot := only(t, manager)

	if snapshot.Status != StatusOK || len(snapshot.Tools) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	if want := "Fake Server. Use it for testing."; snapshot.Description != want {
		t.Errorf("description = %q, want %q", snapshot.Description, want)
	}

	result, err := manager.Call(t.Context(), &CallRequest{Server: "fake", Tool: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || !strings.Contains(text(result), `"text":"hi"`) {
		t.Fatalf("echo = %v, %v", text(result), err)
	}

	var (
		mu    sync.Mutex
		steps []float64
	)

	progress := func(_ context.Context, params *mcp.ProgressNotificationParams) {
		mu.Lock()
		defer mu.Unlock()

		steps = append(steps, params.Progress)
	}

	if _, err = manager.Call(t.Context(), &CallRequest{Server: "fake", Tool: "steps", Progress: progress}); err != nil {
		t.Fatalf("steps: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// The SDK dispatches notifications concurrently with the response, so late ones are dropped once the call returns.
	if len(steps) == 0 || len(steps) > 3 {
		t.Errorf("progress steps = %v, want between 1 and 3", steps)
	}
}

func TestUnauthorizedAndUnavailable(t *testing.T) {
	t.Parallel()

	denied := testmcp.ServeHTTPWith(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))

	manager := startManager(t, httpServer("locked", denied), httpServer("down", "http://127.0.0.1:1/mcp"))

	statuses := map[string]Status{}
	for _, snapshot := range manager.Snapshots() {
		statuses[snapshot.Name] = snapshot.Status
	}

	if statuses["locked"] != StatusUnauthorized || statuses["down"] != StatusUnavailable {
		t.Fatalf("statuses = %v", statuses)
	}

	_, err := manager.Call(t.Context(), &CallRequest{Server: "down", Tool: "x"})

	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindUnavailable || classified.RetryAfter <= 0 {
		t.Fatalf("call to cooling-down server = %v", err)
	}

	if _, err = manager.Call(t.Context(), &CallRequest{Server: "nope", Tool: "x"}); !errors.As(err, &classified) || classified.Kind != KindUnknownServer {
		t.Fatalf("unknown server = %v", err)
	}
}

func TestStdioCrashRecovery(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	server := &config.Server{
		Name: "local", Transport: config.TransportStdio, Enabled: true, Command: executable,
		Args: []string{"-test.run=^$"}, Env: map[string]string{testmcp.StdioEnv: "1"},
	}
	manager := startManager(t, server)

	if snapshot := only(t, manager); snapshot.Status != StatusOK || len(snapshot.Tools) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	_, err = manager.Call(t.Context(), &CallRequest{Server: "local", Tool: "crash"})

	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != KindUnavailable || !classified.MayHaveRun {
		t.Fatalf("crash = %v, want unavailable with MayHaveRun", err)
	}

	result, err := manager.Call(t.Context(), &CallRequest{Server: "local", Tool: "echo", Arguments: map[string]any{"n": 1}})
	if err != nil || !strings.Contains(text(result), `"n":1`) {
		t.Fatalf("echo after crash = %v, %v", text(result), err)
	}
}

func TestSessionIsolation(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	server := &config.Server{
		Name: "local", Transport: config.TransportStdio, Enabled: true, Command: executable, Isolation: config.IsolationSession,
		Args: []string{"-test.run=^$"}, Env: map[string]string{testmcp.StdioEnv: "1"},
	}
	manager := startManager(t, server)

	pid := func(session string) string {
		t.Helper()

		result, callErr := manager.Call(t.Context(), &CallRequest{Server: "local", Tool: "pid", Session: session})
		if callErr != nil {
			t.Fatalf("pid for session %q: %v", session, callErr)
		}

		return text(result)
	}

	first, again, other, shared := pid("a"), pid("a"), pid("b"), pid("")
	if first != again || first == other || shared == first || shared == other {
		t.Fatalf("pids: first=%s again=%s other=%s shared=%s", first, again, other, shared)
	}

	manager.CloseSession("a")

	if restarted := pid("a"); restarted == first {
		t.Errorf("session a still uses process %s after CloseSession", first)
	}
}

func TestRetryableCallSurvivesDroppedConnection(t *testing.T) {
	t.Parallel()

	var failNext atomic.Bool

	inner := testmcp.HTTPHandler(testmcp.NewServer("flaky", "", "", testmcp.Tool{Name: "read", ReadOnly: true}))
	endpoint := testmcp.ServeHTTPWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && failNext.CompareAndSwap(true, false) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("response writer cannot hijack")

				return
			}

			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}

			return
		}

		inner.ServeHTTP(w, r)
	}))

	manager := startManager(t, httpServer("flaky", endpoint))

	failNext.Store(true)

	result, err := manager.Call(t.Context(), &CallRequest{Server: "flaky", Tool: "read", Retryable: true})
	if err != nil || !strings.Contains(text(result), "read") {
		t.Fatalf("retryable call = %v, %v", text(result), err)
	}
}

func TestDescriptionPrefersConfig(t *testing.T) {
	t.Parallel()

	init := &mcp.InitializeResult{ServerInfo: &mcp.Implementation{Title: "Title."}, Instructions: "First. Second."}
	if got := describeServer(&config.Server{Description: "Configured."}, init); got != "Configured." {
		t.Errorf("configured = %q", got)
	}

	if got := describeServer(&config.Server{}, init); got != "Title. First." {
		t.Errorf("derived = %q", got)
	}

	if got := describeServer(&config.Server{}, &mcp.InitializeResult{}); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestBackoff(t *testing.T) {
	t.Parallel()

	for failures, want := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 5: 32 * time.Second, 6: time.Minute, 40: time.Minute} {
		if got := backoff(failures); got != want {
			t.Errorf("backoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

func TestChildEnvAllowList(t *testing.T) {
	t.Parallel()

	env := childEnv([]string{"PATH=/bin", "MCP_COMPENDIUM_SECRET=x", "HOME=/home/u", "DISPLAY=:0"}, map[string]string{"HOME": "/override", "EXTRA": "1"})
	joined := "|" + strings.Join(env, "|") + "|"

	for _, want := range []string{"|PATH=/bin|", "|HOME=/override|", "|EXTRA=1|"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env %v missing %s", env, want)
		}
	}

	for _, unwanted := range []string{"MCP_COMPENDIUM_SECRET", "DISPLAY", "HOME=/home/u"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("env %v leaks %s", env, unwanted)
		}
	}
}

func startManager(t *testing.T, servers ...*config.Server) *Manager {
	t.Helper()

	manager := New(servers, Settings{StartupTimeout: 10 * time.Second, ExecutionTimeout: 10 * time.Second}, nil, logging.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(func() {
		cancel()
		manager.Close()
	})

	if err := manager.WaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}

	return manager
}

func only(t *testing.T, manager *Manager) Snapshot {
	t.Helper()

	snapshots := manager.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}

	return snapshots[0]
}

func httpServer(name, raw string) *config.Server {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}

	return &config.Server{Name: name, Transport: config.TransportHTTP, Enabled: true, URL: parsed}
}

func text(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}

	var builder strings.Builder

	for _, content := range result.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(block.Text)
		}
	}

	return builder.String()
}
