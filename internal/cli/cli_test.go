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

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"

	"github.com/steelshot/mcp-compendium/internal/config"
	"github.com/steelshot/mcp-compendium/internal/downstream"
	"github.com/steelshot/mcp-compendium/internal/logging"
	"github.com/steelshot/mcp-compendium/internal/router"
	"github.com/steelshot/mcp-compendium/internal/server"
	"github.com/steelshot/mcp-compendium/internal/store"
	"github.com/steelshot/mcp-compendium/internal/testmcp"
)

// syncBuffer is a bytes.Buffer safe for one writer and concurrent readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func TestMain(m *testing.M) {
	if code, ok := testmcp.RunStdioIfRequested(func() *mcp.Server {
		return testmcp.NewServer(
			"files",
			"Files",
			"Reads local files.",
			testmcp.Tool{
				Name:        "read_file",
				Description: "Read a file.",
				ReadOnly:    true,
				Properties:  map[string]string{"path": "string"},
				Required:    []string{"path"},
			},
		)
	}); ok {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestDaemonServesAgents(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	remote := testmcp.ServeHTTP(t, testmcp.NewServer("tickets", "", "", testmcp.Tool{Name: "create_ticket", Description: "Create a ticket."}))

	dir := t.TempDir()
	writeConfig(
		t,
		dir,
		"10-files.yaml",
		"servers:\n  files:\n    type: stdio\n    command: '"+executable+"'\n    args: ['-test.run=^$']\n    env: {"+testmcp.StdioEnv+": '1'}\n",
	)
	writeConfig(t, dir, "20-tickets.json", `{"servers": {"tickets": {"type": "http", "url": "`+remote+`", "description": "Ticket tracker."}}}`)

	address := startDaemon(t, dir)
	base := "http://" + address

	waitReady(t, base)

	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "test"}, nil)

	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: base + mcpPath}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools/list = %v, %v", tools, err)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_tools", Arguments: map[string]any{"query": "read a file"}})
	if err != nil || !strings.HasPrefix(textOf(result), "files read_file(path: string) [read-only]") {
		t.Fatalf("search = %q, %v", textOf(result), err)
	}

	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_tool", Arguments: map[string]any{"server": "tickets", "tool": "create_ticket", "arguments": map[string]any{"title": "x"}},
	})
	if err != nil || result.IsError || !strings.Contains(textOf(result), `"title":"x"`) {
		t.Fatalf("call = %q, %v", textOf(result), err)
	}

	// Cross-origin browser requests are rejected.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+mcpPath, strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want 403", resp.StatusCode)
	}
}

func TestCheckExposure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		listen string
		auth   config.Auth
		allow  bool
	}{
		{listen: "127.0.0.1:8080", allow: true},
		{listen: "[::1]:8080", allow: true},
		{listen: "localhost:8080", allow: true},
		{listen: ":8080", allow: false},
		{listen: "0.0.0.0:8080", allow: false},
		{listen: ":8080", auth: config.Auth{AllowAnonymous: true}, allow: true},
		{listen: ":8080", auth: config.Auth{Providers: []*config.Provider{{Name: "p"}}}, allow: true},
	}

	for _, test := range cases {
		err := checkExposure(server.Settings{Listen: test.listen}, test.auth)
		if test.allow != (err == nil) || (err != nil && !errors.Is(err, errAnonymousExposure)) {
			t.Errorf("%s %+v: err = %v", test.listen, test.auth, err)
		}
	}
}

func TestSettingsPrecedence(t *testing.T) {
	t.Parallel()

	cmd := newServeCommand(streams{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}})
	if err := cmd.ParseFlags([]string{"--execution-timeout=2m"}); err != nil {
		t.Fatal(err)
	}

	environ := func() []string {
		return []string{"MCP_COMPENDIUM_STARTUP_TIMEOUT=5s", "MCP_COMPENDIUM_EXECUTION_TIMEOUT=9m", "MCP_COMPENDIUM_UNKNOWN=x", "OTHER=y"}
	}

	k, err := loadSettings(cmd.Flags(), environ, settingsModule{defaults: downstream.Defaults, envKeys: downstream.EnvKeys})
	if err != nil {
		t.Fatal(err)
	}

	settings, err := downstream.Load(k)
	if err != nil {
		t.Fatal(err)
	}

	// Environment beats defaults; an explicitly set flag beats the environment.
	if settings.StartupTimeout != 5*time.Second || settings.ExecutionTimeout != 2*time.Minute {
		t.Errorf("settings = %+v", settings)
	}

	if k.Exists("") || k.Exists("unknown") {
		t.Error("unknown environment variables must be ignored")
	}
}

func TestLoginStatusLogoutThroughDaemon(t *testing.T) {
	t.Parallel()

	fake := testmcp.NewAuthServer(t)
	dir := t.TempDir()
	writeConfig(t, dir, "config.yaml", "servers:\n  secure: {type: http, url: '"+fake.MCPURL()+"'}\n")

	base := "http://" + startDaemon(t, dir)
	waitReady(t, base)

	if output := run(t, "status", "--url", base); !strings.Contains(output, "unauthorized") || !strings.Contains(output, "mcpcd login secure") {
		t.Fatalf("status before login:\n%s", output)
	}

	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { _ = stdinWriter.Close() })

	var stdout, stderr syncBuffer

	done := make(chan int, 1)

	go func() {
		done <- Execute(t.Context(), []string{"login", "secure", "--url", base, "--no-browser", "--callback-port", "0"}, stdinReader, &stdout, &stderr)
	}()

	authorizationURL := waitFor(t, &stderr, regexp.MustCompile(`https?://\S+/authorize\?\S+`))

	// Play the browser: the authorization server redirects to the CLI's loopback listener.
	callback := testmcp.Authorize(t, authorizationURL)

	resp, err := http.Get(callback.String()) //nolint:noctx // test-only browser stand-in
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if code := <-done; code != 0 || !strings.Contains(stdout.String(), "Logged in to secure") {
		t.Fatalf("login exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	deadline := time.Now().Add(15 * time.Second)
	for output := ""; ; time.Sleep(100 * time.Millisecond) {
		output = run(t, "status", "--url", base)
		if strings.Contains(output, " ok ") && strings.Contains(output, "oauth") {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("server never became ok after login:\n%s", output)
		}
	}

	if output := run(t, "tools", "secure", "--url", base); !strings.Contains(output, "whoami") {
		t.Errorf("tools:\n%s", output)
	}

	if output := run(t, "logout", "secure", "--url", base); !strings.Contains(output, "Logged out of secure") {
		t.Errorf("logout: %s", output)
	}
}

func TestAdminRequiresAdminRole(t *testing.T) {
	t.Parallel()

	userToken, adminToken := strings.Repeat("u", 32), strings.Repeat("a", 32)
	dir := t.TempDir()
	writeConfig(t, dir, "config.yaml", "servers: {}\nauth:\n  providers:\n    - name: tokens\n      type: bearer\n      tokens:\n"+
		"        - {token: "+userToken+", roles: [user]}\n        - {token: "+adminToken+", roles: [user, admin]}\n")

	base := "http://" + startDaemon(t, dir)

	cases := map[string]string{"": "(401)", userToken: "(403)", adminToken: "SERVER"}
	for token, want := range cases {
		var stdout, stderr bytes.Buffer

		args := []string{"status", "--url", base}
		if token != "" {
			args = append(args, "--token", token)
		}

		Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr)

		if !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Errorf("token %q: want %q in\n%s%s", token[:min(len(token), 4)], want, stdout.String(), stderr.String())
		}
	}
}

func TestAgentSignInThroughElicitation(t *testing.T) {
	t.Parallel()

	fake := testmcp.NewAuthServer(t)
	dir := t.TempDir()
	writeConfig(t, dir, "config.yaml", "servers:\n  secure: {type: http, url: '"+fake.MCPURL()+"'}\n")

	listen := "127.0.0.1:" + freePort(t)
	public, _ := url.Parse("http://" + listen)
	base := "http://" + startDaemonWith(t, dir, func(settings *daemonSettings) {
		settings.server.Listen = listen
		settings.server.PublicURL = public
	})
	waitReady(t, base)

	var elicited atomic.Int32

	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "test"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
		ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			elicited.Add(1)

			if req.Params.Mode != "url" || !strings.HasPrefix(req.Params.URL, base+"/oauth/login/") {
				return &mcp.ElicitResult{Action: "decline"}, nil
			}

			// Play the user: the daemon's link leads to the authorization server, which redirects back to the daemon.
			authorize := redirectOf(ctx, t, req.Params.URL)
			callback := testmcp.Authorize(t, authorize) //nolint:contextcheck // test helper uses t.Context()

			resp, err := http.Get(callback.String()) //nolint:noctx // test-only browser stand-in
			if err != nil {
				return nil, err
			}

			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return &mcp.ElicitResult{Action: "cancel"}, nil
			}

			return &mcp.ElicitResult{Action: "accept"}, nil
		},
	})

	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: base + mcpPath}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = session.Close() }()

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "call_tool", Arguments: map[string]any{"server": "secure", "tool": "whoami"},
	})
	if err != nil || result.IsError || !strings.HasPrefix(textOf(result), "whoami") {
		t.Fatalf("call after sign-in = %q, %v", textOf(result), err)
	}

	if elicited.Load() != 1 {
		t.Errorf("elicitations = %d, want 1", elicited.Load())
	}

	// A client without URL elicitation gets the CLI hint instead.
	plain := mcp.NewClient(&mcp.Implementation{Name: "plain", Version: "test"}, nil)

	plainSession, err := plain.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: base + mcpPath}, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = plainSession.Close() }()

	if _, err = run2(t, "logout", "secure", "--url", base); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		result, err = plainSession.CallTool(
			t.Context(),
			&mcp.CallToolParams{Name: "call_tool", Arguments: map[string]any{"server": "secure", "tool": "whoami"}},
		)
		if err == nil && result.IsError && strings.Contains(textOf(result), "mcpcd login secure") {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("plain client after logout = %q, %v", textOf(result), err)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func TestConfigHashPassword(t *testing.T) {
	t.Parallel()

	hash := func(stdin string, args ...string) (string, int, string) {
		var stdout, stderr bytes.Buffer

		code := Execute(t.Context(), append([]string{"config", "hash-password"}, args...), strings.NewReader(stdin), &stdout, &stderr)

		return strings.TrimSpace(stdout.String()), code, stderr.String()
	}

	output, code, stderr := hash("correct-horse-battery\n", "--cost", "10")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	if cost, err := bcrypt.Cost([]byte(output)); err != nil || cost != 10 {
		t.Fatalf("hash %q has cost %d, %v", output, cost, err)
	}

	if bcrypt.CompareHashAndPassword([]byte(output), []byte("correct-horse-battery")) != nil {
		t.Fatalf("hash %q does not match the password", output)
	}

	if _, code, stderr = hash("short\n"); code == 0 || !strings.Contains(stderr, "at least 12 characters") {
		t.Errorf("short password: exit %d, %s", code, stderr)
	}

	if _, code, stderr = hash("correct-horse-battery\n", "--cost", "4"); code == 0 || !strings.Contains(stderr, "between 10 and 31") {
		t.Errorf("low cost: exit %d, %s", code, stderr)
	}
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func startDaemon(t *testing.T, configPath string) string {
	t.Helper()

	return startDaemonWith(t, configPath, nil)
}

func startDaemonWith(t *testing.T, configPath string, adjust func(*daemonSettings)) string {
	t.Helper()

	settings := daemonSettings{
		server:     server.Settings{Listen: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second},
		config:     config.Settings{Path: configPath},
		downstream: downstream.Settings{StartupTimeout: 20 * time.Second, ExecutionTimeout: 20 * time.Second},
		router:     router.Settings{MaxResultBytes: router.DefMaxResultBytes},
		store:      store.Settings{Dir: t.TempDir()},
	}

	if adjust != nil {
		adjust(&settings)
	}

	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan string, 1)
	done := make(chan error, 1)

	go func() { done <- runDaemon(ctx, &settings, logging.Discard(), listening) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("daemon: %v", err)
		}
	})

	select {
	case address := <-listening:
		return address
	case err := <-done:
		t.Fatalf("daemon exited: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not start listening")
	}

	return ""
}

func waitReady(t *testing.T, base string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if probe(t.Context(), base+"/readyz") == nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("daemon never became ready")
}

func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func textOf(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}

	var builder strings.Builder

	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}

	return builder.String()
}

func waitFor(t *testing.T, buffer *syncBuffer, pattern *regexp.Regexp) string {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if match := pattern.FindString(buffer.String()); match != "" {
			return match
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s never appeared in:\n%s", pattern, buffer.String())

	return ""
}

func run(t *testing.T, args ...string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("%v exited %d: %s", args, code, stderr.String())
	}

	return stdout.String()
}

func run2(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		return "", fmt.Errorf("%v exited %d: %s", args, code, stderr.String())
	}

	return stdout.String(), nil
}

// redirectOf requests target without following redirects and returns where it redirects to.
func redirectOf(ctx context.Context, t *testing.T, target string) string {
	t.Helper()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("%s answered %s", target, resp.Status)
	}

	return resp.Header.Get("Location")
}

func freePort(t *testing.T) string {
	t.Helper()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = listener.Close() }()

	_, port, _ := net.SplitHostPort(listener.Addr().String())

	return port
}
