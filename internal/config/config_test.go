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

package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/steelshot/mcp-compendium/schema"
)

const fullYAML = `# yaml-language-server: $schema=../schema/config.schema.json
servers:
  github:
    type: http
    url: https://api.githubcopilot.com/mcp/
    description: GitHub repositories and issues.
    headers:
      Authorization: Bearer ${env:GITHUB_TOKEN}
    tools:
      exclude: ["*delete*"]
      readOnly: true
    limits: { maxResultBytes: 65536, startupTimeout: 10s, executionTimeout: 2m }
  playwright:
    type: stdio
    command: mcp-server-playwright
    args: ["--headless"]
    env: { DISPLAY: "${env:DISPLAY:-:99}" }
    isolation: session
  internal:
    type: http
    url: ${env:INTERNAL_URL}
    oauth:
      clientId: client
      clientSecret: ${file:secret.txt}
      scopes: [read, write]
  legacy:
    enabled: false
    type: sse
    url: https://legacy.example/sse
auth:
  allowAnonymous: false
  providers:
    - name: workloads
      type: jwt
      issuer: https://kubernetes.default.svc.cluster.local
      audiences: [mcp-compendium]
      jwks:
        caFile: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
        bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      grants:
        - match: { sub: "system:serviceaccount:dev:*" }
          roles: [user]
    - name: operator
      type: bearer
      tokens:
        - token: 0123456789abcdef0123456789abcdef
          roles: [user, admin]
    - name: people
      type: basic
      users:
        - username: alice
          passwordHash: $2a$10$3WXiRU.b87H68hWJpcSnvepaS0mUK3W7fNJIDEIK4OHA4V1P8XRVW
          roles: [admin]
`

func TestLoadFullYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "secret.txt", "s3cret\n")
	path := writeFile(t, dir, "config.yaml", strings.ReplaceAll(fullYAML, "${file:secret.txt}", "${file:"+filepath.Join(dir, "secret.txt")+"}"))

	config, err := testLoader(map[string]string{"GITHUB_TOKEN": "gh", "INTERNAL_URL": "https://internal.example/mcp"}).load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	names := make([]string, 0, len(config.Servers))
	for _, server := range config.Servers {
		names = append(names, server.Name)
	}

	if got, want := strings.Join(names, ","), "github,internal,legacy,playwright"; got != want {
		t.Fatalf("servers = %s, want %s", got, want)
	}

	github := mustServer(t, config, "github")
	if github.Headers["Authorization"] != "Bearer gh" || github.URL.Host != "api.githubcopilot.com" || !github.Tools.ReadOnly {
		t.Errorf("github = %+v", github)
	}

	if github.Limits != (Limits{MaxResultBytes: 65536, StartupTimeout: 10 * time.Second, ExecutionTimeout: 2 * time.Minute}) {
		t.Errorf("github limits = %+v", github.Limits)
	}

	playwright := mustServer(t, config, "playwright")
	if playwright.Env["DISPLAY"] != ":99" || playwright.Isolation != IsolationSession || playwright.Args[0] != "--headless" {
		t.Errorf("playwright = %+v", playwright)
	}

	if internal := mustServer(t, config, "internal"); internal.OAuth.ClientSecret != "s3cret" || len(internal.OAuth.Scopes) != 2 {
		t.Errorf("internal oauth = %+v", internal.OAuth)
	}

	if mustServer(t, config, "legacy").Enabled || len(config.EnabledServers()) != 3 {
		t.Error("legacy should be the only disabled server")
	}

	if got := len(config.Auth.Providers); got != 3 {
		t.Fatalf("providers = %d, want 3", got)
	}

	if jwt := config.Auth.Providers[0].JWT; jwt.SubjectClaim != "sub" || jwt.Grants[0].Roles[0] != RoleUser {
		t.Errorf("jwt = %+v", jwt)
	}
}

func TestLoadFormatsAgree(t *testing.T) {
	t.Parallel()

	documents := map[string]string{
		"config.json": `{"servers": {"files": {"type": "stdio", "command": "fs", "args": ["/data"], "tools": {"include": ["read*"]}}}}`,
		"config.jsonc": `{
			// comments and trailing commas are allowed
			"servers": {"files": {"type": "stdio", "command": "fs", "args": ["/data",], "tools": {"include": ["read*"]}}},
		}`,
		"config.yaml": "servers:\n  files:\n    type: stdio\n    command: fs\n    args: [/data]\n    tools:\n      include: [read*]\n",
		"config.toml": "[servers.files]\ntype = \"stdio\"\ncommand = \"fs\"\nargs = [\"/data\"]\n\n[servers.files.tools]\ninclude = [\"read*\"]\n",
	}

	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config, err := testLoader(nil).load(writeFile(t, t.TempDir(), name, document))
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			files := config.Server("files")
			if files == nil || files.Command != "fs" || files.Args[0] != "/data" || files.Tools.Include[0] != "read*" {
				t.Fatalf("files = %+v", files)
			}
		})
	}
}

func TestLoadDirectoryMerges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "10-a.yaml", "servers:\n  a: {type: stdio, command: a}\nauth:\n  allowAnonymous: true\n")
	writeFile(t, dir, "20-b.json", `{"servers": {"b": {"type": "http", "url": "http://b.example/mcp"}}}`)
	writeFile(t, dir, ".hidden.yaml", "servers:\n  hidden: {type: stdio, command: h}\n")
	writeFile(t, dir, "README.md", "ignored")

	config, err := testLoader(nil).load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(config.Servers) != 2 || config.Server("hidden") != nil || !config.Auth.AllowAnonymous || len(config.Sources) != 2 {
		t.Fatalf("config = %+v", config)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "10-a.yaml", `servers:
  a:
    type: stdio
    command: a
    url: http://nope
    bogus: 1
  b:
    type: http
  c:
    type: carrier-pigeon
  "bad name":
    type: stdio
    command: x
  d:
    type: http
    url: ftp://d
    limits: { startupTimeout: soon, maxResultBytes: 10 }
    tools: { include: ["[unclosed"] }
  e:
    type: stdio
    command: ${env:MISSING}
    env: { "1BAD": x }
  f:
    type: http
    url: https://f.example
    headers: { "Bad Name": x, X-Secret: "line\nbreak" }
auth:
  allowAnonymous: true
  providers:
    - name: weak
      type: bearer
      tokens: [{ token: short, roles: [root] }]
    - name: people
      type: basic
      users:
        - { username: a, password: plaintext-password, roles: [user] }
        - { username: b, passwordHash: not-a-hash, roles: [user] }
        - { username: c, passwordHash: $2a$04$XgGHmbqho/NZyrUCOVLNQeEMH2WrOq6JIb02FYShZt9cviSOCosum, roles: [user] }
`)
	writeFile(t, dir, "20-b.yaml", "servers:\n  a: {type: stdio, command: again}\nauth:\n  allowAnonymous: false\n")

	_, err := testLoader(nil).load(dir)

	var configErr *Error
	if !errors.As(err, &configErr) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want *Error", err)
	}

	wants := []string{
		`servers.a.url: not allowed for type stdio`,
		`servers.a.bogus: unknown field`,
		`servers.b: missing required field "url"`,
		`servers.c.type: must be stdio, http or sse, got "carrier-pigeon"`,
		`servers["bad name"]: server names must match`,
		`servers.d.url: must be an absolute http or https URL`,
		`servers.d.limits.startupTimeout: "soon" is not a positive duration`,
		`servers.d.limits.maxResultBytes: must be at least 1024`,
		`servers.d.tools.include[0]: invalid glob "[unclosed"`,
		`servers.e.command: placeholder: environment variable MISSING is not set`,
		`servers.e.env["1BAD"]: is not a valid environment variable name`,
		`servers.f.headers["Bad Name"]: is not a valid HTTP header name`,
		`servers.f.headers.X-Secret: contains characters not allowed in an HTTP header value`,
		`auth.providers[0].tokens[0].token: must be at least 32 characters`,
		`auth.providers[0].tokens[0].roles[0]: must be user or admin, got "root"`,
		`auth.providers[1].users[0].password: not allowed as plaintext`,
		`auth.providers[1].users[0]: missing required field "passwordHash"`,
		`auth.providers[1].users[1].passwordHash: must be a bcrypt hash`,
		`auth.providers[1].users[2].passwordHash: bcrypt cost 4 is below the minimum of 10`,
		`20-b.yaml: servers.a: already defined in`,
		`20-b.yaml: auth.allowAnonymous: already set in`,
	}

	message := err.Error()
	for _, want := range wants {
		if !strings.Contains(filepath.ToSlash(message), want) {
			t.Errorf("error missing %q\n%s", want, message)
		}
	}
}

func TestLoadDecodeErrors(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"dup.json":   `{"servers": {}, "servers": {}}`,
		"dup.yaml":   "servers: {}\nservers: {}\n",
		"list.json":  `[1, 2]`,
		"bad.toml":   "servers = [",
		"date.toml":  "when = 2026-01-01\n",
		"trail.json": `{} {}`,
	}

	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := testLoader(nil).load(writeFile(t, t.TempDir(), name, document)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoadMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := Read(filepath.Join(dir, "absent.yaml")); !errors.Is(err, errNoFiles) {
		t.Errorf("missing file err = %v", err)
	}

	if _, err := Read(dir); !errors.Is(err, errNoFiles) {
		t.Errorf("empty dir err = %v", err)
	}

	if _, err := Read(writeFile(t, dir, "config.ini", "")); !errors.Is(err, errNoFiles) {
		t.Errorf("unsupported extension err = %v", err)
	}
}

func TestExpandString(t *testing.T) {
	t.Parallel()

	files := map[string]string{"/run/secret": "value\r\n"}
	e := newExpander(
		func(name string) (string, bool) {
			value, ok := map[string]string{"SET": "set", "EMPTY": ""}[name]

			return value, ok
		},
		func(path string) ([]byte, error) {
			if content, ok := files[path]; ok {
				return []byte(content), nil
			}

			return nil, fs.ErrNotExist
		},
	)

	cases := []struct {
		input, want string
		fails       bool
	}{
		{input: "plain", want: "plain"},
		{input: "a ${env:SET} b", want: "a set b"},
		{input: "${env:EMPTY:-fallback}", want: "fallback"},
		{input: "${env:UNSET:-}", want: ""},
		{input: "${env:UNSET:-x:-y}", want: "x:-y"},
		{input: "$${env:SET}", want: "${env:SET}"},
		{input: "${file:/run/secret}", want: "value"},
		{input: "${env:UNSET}", fails: true},
		{input: "${UNSET}", fails: true},
		{input: "${vault:x}", fails: true},
		{input: "${env:SET", fails: true},
		{input: "${env:1X}", fails: true},
		{input: "${file:/missing}", fails: true},
	}

	for _, test := range cases {
		got, err := e.expandString(test.input)
		if test.fails {
			if err == nil {
				t.Errorf("%q: expected an error, got %q", test.input, got)
			}

			continue
		}

		if err != nil || got != test.want {
			t.Errorf("%q = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
}

// TestSchemaAgrees checks the published schema accepts the documents the loader accepts and rejects a sample of the
// documents the loader rejects, so editors and the daemon agree on the format.
func TestSchemaAgrees(t *testing.T) {
	t.Parallel()

	var root jsonschema.Schema
	if err := json.Unmarshal(schema.Config, &root); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	resolved, err := root.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}

	valid := []string{
		fullYAML,
		"servers: {}\n",
		"servers:\n  a: {type: http, url: 'http://a/mcp', tools: {readOnly: true}, isolation: shared}\n",
	}
	invalid := []string{
		"servers:\n  a: {type: stdio, command: a, url: 'http://a'}\n",
		"servers:\n  a: {type: http}\n",
		"servers:\n  a: {type: http, url: 'http://a', bogus: 1}\n",
		"servers:\n  a: {type: http, url: 'http://a', limits: {startupTimeout: soon}}\n",
		"auth:\n  providers: [{name: x, type: bearer}]\n",
		"auth:\n  providers: [{name: x, type: jwt, issuer: 'https://i', audiences: [a], grants: [{match: {sub: '*'}, roles: [root]}]}]\n",
	}

	stubFiles := func(string) ([]byte, error) { return []byte("s3cret"), nil }
	env := map[string]string{"GITHUB_TOKEN": "x", "INTERNAL_URL": "http://i"}
	accepting := loader{expander: newExpander(func(name string) (string, bool) { value, ok := env[name]; return value, ok }, stubFiles)}

	for index, document := range valid {
		tree, err := decode("x.yaml", []byte(document))
		if err != nil {
			t.Fatalf("valid[%d]: decode: %v", index, err)
		}

		if err := resolved.Validate(toSchemaValue(tree)); err != nil {
			t.Errorf("valid[%d]: schema rejected: %v", index, err)
		}

		if err := accepting.loadTree(tree); err != nil {
			t.Errorf("valid[%d]: loader rejected: %v", index, err)
		}
	}

	for index, document := range invalid {
		tree, err := decode("x.yaml", []byte(document))
		if err != nil {
			t.Fatalf("invalid[%d]: decode: %v", index, err)
		}

		if err := resolved.Validate(toSchemaValue(tree)); err == nil {
			t.Errorf("invalid[%d]: schema accepted", index)
		}

		if err := testLoader(nil).loadTree(tree); err == nil {
			t.Errorf("invalid[%d]: loader accepted", index)
		}
	}
}

// toSchemaValue converts int64 to float64, the number type jsonschema-go expects from encoding/json.
func toSchemaValue(value any) any {
	switch typed := value.(type) {
	case int64:
		return float64(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = toSchemaValue(item)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = toSchemaValue(item)
		}

		return out
	default:
		return value
	}
}

func testLoader(env map[string]string) loader {
	lookup := func(name string) (string, bool) {
		value, ok := env[name]

		return value, ok
	}

	return loader{expander: newExpander(lookup, os.ReadFile), readFile: os.ReadFile}
}

// mustServer returns the named server, failing the test when the configuration has no such server.
func mustServer(t *testing.T, config *Config, name string) *Server {
	t.Helper()

	server := config.Server(name)
	if server == nil {
		t.Fatalf("server %q is missing", name)
	}

	return server
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// loadTree runs placeholder expansion, parsing and merging on an already decoded tree.
func (l loader) loadTree(tree map[string]any) error {
	var list []Problem

	report := newProblems("test", &list)
	l.expander.expandTree(tree, "", report)
	merge([]fragment{parseFragment("test", tree, report)}, &list)

	return asError(list)
}
