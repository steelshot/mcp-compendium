# Authentication

On a loopback address the daemon needs no authentication. On any other address it refuses to start unless
`auth.providers` is configured or `auth.allowAnonymous: true` states that a firewall or network policy already
restricts access.

Providers can be combined; each grants the `user` role (the MCP endpoint), the `admin` role (the admin API used by the
CLI) or both. MCP sessions are bound to the principal that created them.

```yaml
auth:
  providers:
    # Kubernetes service account tokens, or any OIDC issuer. Keys are discovered lazily, so an unreachable issuer does
    # not stop the daemon from starting.
    - name: workloads
      type: jwt
      issuer: https://kubernetes.default.svc.cluster.local
      audiences: [ mcp-compendium ]
      jwks: # only needed when discovery itself requires credentials
        caFile: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
        bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      grants:
        - match: { sub: "system:serviceaccount:agents:*" }
          roles: [ user ]
        - match: { groups: platform-admins }    # string-array claims match when any element matches
          roles: [ user, admin ]

    - name: operators
      type: bearer
      tokens:
        - subject: ci
          token: ${file:/run/secrets/admin-token}   # at least 32 characters: openssl rand -hex 32
          roles: [ user, admin ]

    - name: people
      type: basic
      users:
        - username: alice
          passwordHash: ${file:/run/secrets/alice-password-hash}   # bcrypt, cost 10 or more
          roles: [ admin ]
```

Basic users are configured with a bcrypt hash of their password, never the password itself. Create one with
`printf %s "$PASSWORD" | mcpcd config hash-password` (the container images include `mcpcd`), or `htpasswd -nbBC 12 ''
"$PASSWORD" | cut -d: -f2`. The daemon remembers successful checks for a few minutes, so clients that send credentials
with every request pay the bcrypt cost only occasionally.

Workloads in the cluster authenticate with a projected service account token whose audience matches:

```yaml
volumes:
  - name: mcp-token
    projected:
      sources:
        - serviceAccountToken: { audience: mcp-compendium, path: token, expirationSeconds: 3600 }
```

With a `jwt` provider, the daemon also publishes [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728) protected resource
metadata, so OAuth-capable MCP clients can obtain tokens from the issuer themselves.
