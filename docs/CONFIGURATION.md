# Configuration

Settings that describe *where* and *how* the daemon runs are flags or environment variables. The servers and inbound
authentication live in a configuration file.

## Environment and flags

Every flag has an environment variable; an explicitly set flag wins over the environment, which wins over the default.

| Variable                           | Flag                  | Default                                                                                                  |
|------------------------------------|-----------------------|----------------------------------------------------------------------------------------------------------|
| `MCP_COMPENDIUM_CONFIG`            | `--config`, `-c`      | `<user config dir>/mcp-compendium`; `/etc/mcp-compendium` in images                                      |
| `MCP_COMPENDIUM_STATE_DIR`         | `--state-dir`         | `$XDG_STATE_HOME/mcp-compendium` or `~/.local/state/mcp-compendium`; `/var/lib/mcp-compendium` in images |
| `MCP_COMPENDIUM_LISTEN`            | `--listen`            | `127.0.0.1:8080`; `:8080` in images                                                                      |
| `MCP_COMPENDIUM_PUBLIC_URL`        | `--public-url`        | unset; the browser-reachable base URL, which enables sign-in links                                       |
| `MCP_COMPENDIUM_LOG_LEVEL`         | `--log-level`         | `info` (`debug`, `info`, `warn`, `error`)                                                                |
| `MCP_COMPENDIUM_LOG_FORMAT`        | `--log-format`        | `json` (`json`, `text`)                                                                                  |
| `MCP_COMPENDIUM_STARTUP_TIMEOUT`   | `--startup-timeout`   | `30s`: connecting to a server and listing its tools                                                      |
| `MCP_COMPENDIUM_EXECUTION_TIMEOUT` | `--execution-timeout` | `1h`: one tool call                                                                                      |
| `MCP_COMPENDIUM_SHUTDOWN_TIMEOUT`  | `--shutdown-timeout`  | `15s`: draining in-flight requests                                                                       |
| `MCP_COMPENDIUM_MAX_RESULT_BYTES`  | `--max-result-bytes`  | `32768`                                                                                                  |

The client commands read `MCP_COMPENDIUM_URL` (default `http://127.0.0.1:8080`) and credentials from
`MCP_COMPENDIUM_TOKEN`, `MCP_COMPENDIUM_TOKEN_FILE`, or `MCP_COMPENDIUM_USERNAME` and `MCP_COMPENDIUM_PASSWORD`.

## Configuration file

`MCP_COMPENDIUM_CONFIG` names a file, or a directory whose `*.yaml`, `*.yml`, `*.json`, `*.jsonc` and `*.toml` files
are merged in name order (hidden files are skipped, so Kubernetes ConfigMap mounts work). A server or auth provider
defined in two files is an error, never a silent override. Every problem in every file is reported at once, with its
file and path:

```text
Error: invalid configuration:
  conf.d/10-github.yaml: servers.github.url: must be an absolute http or https URL
  conf.d/20-files.yaml: servers.files.command: placeholder: environment variable FS_BIN is not set and ${env:FS_BIN} has no :-default
```

All four formats share one [JSON Schema](../schema/config.schema.json) (`mcpcd config schema` prints it). Editors pick it
up with `# yaml-language-server: $schema=…` in YAML, `"$schema"` in JSON, or `#:schema …` in TOML.

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/steelshot/mcp-compendium/main/schema/config.schema.json
servers:
  github:
    type: http                     # stdio, http (Streamable HTTP) or sse (the deprecated 2024-11-05 transport)
    url: https://api.githubcopilot.com/mcp/
    description: GitHub repositories, issues, pull requests and code search.
    headers:
      Authorization: Bearer ${file:/run/secrets/github-token}
    tools:
      exclude: [ "*delete*" ]        # globs; exclude wins over include
      readOnly: false              # true exposes only tools annotated read-only
    limits:
      maxResultBytes: 65536
      startupTimeout: 30s
      executionTimeout: 10m

  playwright:
    type: stdio
    command: mcp-server-playwright
    args: [ --headless ]
    env:
      DISPLAY: ${env:DISPLAY:-:99}
    isolation: session             # one connection, and one process, per agent session

  internal:
    type: http
    url: ${env:INTERNAL_MCP_URL}
    oauth: # optional: a pre-registered client; otherwise one is registered dynamically
      clientId: mcp-compendium
      clientSecret: ${file:/run/secrets/internal-client-secret}
      scopes: [ read ]

  legacy:
    enabled: false
    type: sse
    url: https://legacy.example/sse

auth:
  allowAnonymous: false
  providers: [ ]                    # see AUTHENTICATION.md
```

| Field                            | Notes                                                                                                                                                                    |
|----------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `description`                    | One or two sentences telling agents what the server is for. Defaults to the server's own title and the first sentence of its instructions.                               |
| `command`, `args`, `env`, `cwd`  | stdio only. The process inherits only a small allow-list of the daemon's environment (`PATH`, `HOME`, locale, proxy and CA variables); pass anything else through `env`. |
| `url`, `headers`, `oauth`        | `http` and `sse` only.                                                                                                                                                   |
| `tools.include`, `tools.exclude` | Glob patterns (`*`, `?`, `[…]`, `{a,b}`) matched against tool names. Hidden tools are reported to agents as disabled, not unknown.                                       |

**Placeholders** follow the OpenTelemetry Collector convention and are resolved in string values when the
configuration is loaded:

| Placeholder            | Resolves to                                                                                         |
|------------------------|-----------------------------------------------------------------------------------------------------|
| `${env:NAME}`          | The environment variable; an error when it is unset or empty                                        |
| `${env:NAME:-default}` | The variable, or `default` when unset or empty                                                      |
| `${file:/path}`        | The file's contents without a trailing newline, for secrets mounted by Kubernetes, Podman or Docker |
| `$${`                  | A literal `${`                                                                                      |

`mcpcd config validate` loads the configuration exactly as the daemon does and summarizes it without printing any
secrets.
