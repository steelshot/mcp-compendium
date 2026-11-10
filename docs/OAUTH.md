# Signing in to OAuth servers

Downstream servers that require OAuth show as `unauthorized` until someone signs in. Tokens are stored in the state
directory, bound to the server's URL, refreshed automatically (including rotated refresh tokens), and survive
restarts. Changing a server's URL invalidates its token.

There are two ways to sign in, and neither needs a rebuild or a restart:

- **From the agent.** When `MCP_COMPENDIUM_PUBLIC_URL` is set and the agent's client supports URL-mode elicitation,
  a call to an unauthorized server returns a sign-in link that the client shows the user. The daemon completes the
  flow at `<public URL>/oauth/callback` and the call continues. Links are single-use, expire after 10 minutes and are
  bound to the session that requested them.
- **From the CLI.** `mcpcd login <server>` asks the daemon to start the flow and opens the authorization page. The
  authorization server redirects to a listener on the CLI's machine, which passes the result to the daemon; the daemon
  holds the PKCE verifier, exchanges the code and stores the tokens. This works through `kubectl port-forward` and
  from another machine (`--no-browser` lets you paste the redirect URL). `--local` signs in without a running daemon
  by writing the state directory directly.

Clients without URL elicitation get an error telling the agent to ask the user to run `mcpcd login <server>`.

Pre-registered OAuth clients must allow the redirect URIs `http://127.0.0.1:33418/oauth/callback` (change the port
with `--callback-port`) and, for sign-in links, `<public URL>/oauth/callback`. Dynamically registered clients need no
set-up.
