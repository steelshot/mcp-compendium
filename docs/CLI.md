# Command line

| Command                                    | Purpose                                                                                  |
|--------------------------------------------|------------------------------------------------------------------------------------------|
| `mcpcd serve`                              | Run the daemon.                                                                          |
| `mcpcd status`                             | Every server's transport, state, exposed and hidden tool counts, and last error.         |
| `mcpcd tools <server>`                     | A server's tools, including those hidden by its filters.                                 |
| `mcpcd login <server>` / `logout <server>` | Sign in to, or forget, an OAuth-protected server.                                        |
| `mcpcd reconnect <server>`                 | Reconnect now, ignoring any cooldown.                                                    |
| `mcpcd config validate [path]`             | Load and validate the configuration.                                                     |
| `mcpcd config schema`                      | Print the configuration JSON Schema.                                                     |
| `mcpcd config hash-password`               | Print the bcrypt hash of a password read from standard input, for a basic provider user. |
| `mcpcd healthcheck [--ready] [--wait <d>]` | Exit non-zero unless `/livez`, or `/readyz` with `--ready`, answers; `--wait` retries.   |
| `mcpcd version`                            | Build information.                                                                       |
| `mcpcd completion <shell>`                 | Shell completion for bash, zsh, fish or PowerShell.                                      |

The client commands use the [admin API](../openapi.yaml) (OpenAPI 3.2), which requires the `admin` role. Errors are
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem documents.

The HTTP listener also serves `/livez` (the process is responsive) and `/readyz` (every server has been tried once).
Container health checks should probe `/livez`, so that an unreachable server never restarts a healthy daemon.
