# Deployment

## Binaries

Each [release](https://github.com/steelshot/mcp-compendium/releases) has archives for Linux (`tar.xz`) and Windows
(`zip`) on x86_64 and arm64, and for macOS on Apple Silicon (`tar.xz`), each containing `mcpcd`, the licence, the
README, this documentation and the changelog. `checksums.txt` holds their SHA-256 sums and is signed with Cosign
(`checksums.txt.sigstore.json`); build provenance is attested and attached as `mcp-compendium-<version>.intoto.jsonl`.
[SECURITY.md](../SECURITY.md#verifying-releases) shows how to verify them:

```bash
sha256sum --check --ignore-missing checksums.txt
```

The macOS binary is not notarised. macOS blocks it when it was downloaded through a browser; once the checksum is
verified, clear the quarantine flag:

```bash
xattr -d com.apple.quarantine mcpcd
```

## Container images

Multi-architecture (amd64, arm64) OCI images are signed with Cosign and carry provenance and SBOM attestations:

| Image                                         | Base                                                     | Use                                      |
|-----------------------------------------------|----------------------------------------------------------|------------------------------------------|
| `ghcr.io/steelshot/mcp-compendium/distroless` | `scratch`: the binary, CA certificates and timezone data | Remote (`http`/`sse`) servers only       |
| `ghcr.io/steelshot/mcp-compendium/minimal`    | Alpine with tini                                         | Stdio servers you add in a derived image |

| Tag                 | Follows                                                         |
|---------------------|-----------------------------------------------------------------|
| `X.Y.Z`, `X.Y`, `X` | A release, or the newest release of that minor or major version |
| `latest`            | The newest release                                              |

Both run as UID 65532 with a read-only root filesystem, read configuration from `/etc/mcp-compendium` and write state
to `/var/lib/mcp-compendium`. The distroless image has no shell, so health checks and `podman exec` must run
`/usr/local/bin/mcpcd` directly, in exec form. Base image updates are released as patch versions. The images contain
no MCP servers, so their dependencies and vulnerabilities stay yours to choose. Derive from `minimal` to add them,
pinning exact versions so rebuilds are reproducible:

```dockerfile
# x-release-please-start-version
FROM ghcr.io/steelshot/mcp-compendium/minimal:0.3.0
# x-release-please-end
USER root
RUN apk add --no-cache nodejs npm && \
    npm install --global --omit=dev --no-audit --no-fund @modelcontextprotocol/server-filesystem@2.2.0 && \
    npm cache clean --force
USER 65532:65532
```

[`images/distroless.Containerfile`](../images/distroless.Containerfile) and
[`images/minimal.Containerfile`](../images/minimal.Containerfile) build the images from a published release rather than
from source: they download the release archive for the target architecture and verify its checksums, and the build
context is empty (see [`images/.containerignore`](../images/.containerignore)).
`VERSION` selects the release and `RELEASES` a mirror of the release downloads:

<!-- x-release-please-start-version -->
```bash
podman build --format oci --platform linux/amd64,linux/arm64 --manifest localhost/mcp-compendium:0.3.0 \
  --build-arg VERSION=0.3.0 -f images/distroless.Containerfile images
```
<!-- x-release-please-end -->

The same pattern suits air-gapped or secret-dependent builds: serve the archives from an internal `RELEASES` URL, or
skip deriving an image entirely and mount configuration and secrets into the published one at runtime.

## Kubernetes

```bash
helm install mcp-compendium oci://ghcr.io/steelshot/charts/mcp-compendium --values values.yaml
```

```yaml
servers:
  github:
    type: http
    url: https://api.githubcopilot.com/mcp/
    headers:
      Authorization: Bearer ${env:GITHUB_TOKEN}
envFrom:
  - secretRef: { name: github-token }
auth:
  providers:
    - name: workloads
      type: jwt
      issuer: https://kubernetes.default.svc.cluster.local
      audiences: [ mcp-compendium ]
      # The in-cluster issuer serves discovery only to authenticated callers over the cluster CA.
      jwks:
        caFile: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
        bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      grants:
        - match: { sub: "system:serviceaccount:agents:*" }
          roles: [ user ]
serviceAccount:
  automount: true                        # provides the CA and token used by jwks above
publicUrl: https://mcp.example.com       # enables sign-in links; route it with ingress or httpRoute
httpRoute:
  enabled: true
  parentRefs: [ { name: gateway, namespace: gateway-system } ]
  hostnames: [ mcp.example.com ]
networkPolicy:
  enabled: true
  from:
    - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: agents } }
```

Each entry in `servers` becomes its own file in the configuration directory; `existingConfigMap` replaces them all.
The chart runs a single replica with the `Recreate` strategy, a persistent volume for tokens, restricted pod security
and no Kubernetes API access. Configuration changes roll the pod. `image.flavour` selects `distroless` (the default)
or `minimal`, and `image.fullRepository` points at a derived image. The image tag defaults to the chart's version. See
[`values.yaml`](../helm/values.yaml) for every option.

Some managed clusters publish a public OIDC issuer for service account tokens; use it as `issuer`, and neither `jwks`
nor `serviceAccount.automount` is then needed.

## Podman

A rootless [Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) unit runs the daemon as a
user service. Quadlet makes it wait until the network is online (Podman 5.3 or later); `network-online.target` itself
has no effect in user units. Save it as `~/.config/containers/systemd/mcp-compendium.container`:

```ini
[Unit]
Description=MCP Compendium

[Container]
ContainerName=mcp-compendium
# Use the minimal image, or one derived from it, for stdio servers.
Image=ghcr.io/steelshot/mcp-compendium/distroless:latest
AutoUpdate=registry
# Only this host can reach the daemon, which is why the configuration allows anonymous access.
PublishPort=127.0.0.1:8080:8080
Volume=%h/.config/mcp-compendium:/etc/mcp-compendium:ro,Z
Volume=mcp-compendium-state:/var/lib/mcp-compendium
Secret=github-token,type=env,target=GITHUB_TOKEN
# Lets agents show sign-in links for servers that need OAuth.
Environment=MCP_COMPENDIUM_PUBLIC_URL=http://127.0.0.1:8080
ReadOnly=true
NoNewPrivileges=true
DropCapability=ALL
# The JSON array (exec form) is required: Podman runs any other value with /bin/sh, which the image does not have.
HealthCmd=["/usr/local/bin/mcpcd", "healthcheck"]
HealthInterval=30s
HealthTimeout=10s
HealthRetries=3
HealthStartPeriod=10s
HealthOnFailure=kill

[Service]
Restart=always
RestartSec=5
TimeoutStartSec=300

[Install]
WantedBy=default.target
```

and the configuration as `~/.config/mcp-compendium/config.yaml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/steelshot/mcp-compendium/main/schema/config.schema.json
servers:
  github:
    type: http
    url: https://api.githubcopilot.com/mcp/
    description: GitHub repositories, issues, pull requests and code search.
    headers:
      Authorization: Bearer ${env:GITHUB_TOKEN}
    tools:
      exclude: [ "*delete*" ]
auth:
  # The unit publishes the port on 127.0.0.1 only. Configure providers instead when other hosts can reach it.
  allowAnonymous: true
```

Then create the secret, validate the configuration, and start the service. Validation runs in the image with the unit's
configuration mount and one `--secret` for each of its `Secret=` lines, so every placeholder resolves as it will in the
unit:

```bash
printf %s "$GITHUB_TOKEN" | podman secret create github-token -
podman run --rm --secret github-token,type=env,target=GITHUB_TOKEN \
  -v ~/.config/mcp-compendium:/etc/mcp-compendium:ro,Z \
  ghcr.io/steelshot/mcp-compendium/distroless:latest config validate
systemctl --user daemon-reload
systemctl --user start mcp-compendium
podman exec mcp-compendium /usr/local/bin/mcpcd healthcheck --ready --wait 60s
```

Validation reports the problems in every file at once, such as unset variables. A YAML syntax error stops the checks of
its file: `mapping values are not allowed in this context` usually means an unquoted value containing `: `, such as a
description; quote it. The health check returns once every server has been tried; `mcpcd status` then shows any server
that is not `connected`. Agents connect to `http://127.0.0.1:8080/mcp`.
