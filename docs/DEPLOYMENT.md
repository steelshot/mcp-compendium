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

## Container image

The multi-architecture (amd64, arm64) OCI image `ghcr.io/steelshot/mcp-compendium/distroless` holds the `mcpcd` binary,
CA certificates and timezone data on an empty base, and serves remote (`http`/`sse`) servers. It is signed with Cosign
and carries provenance and SBOM attestations.

| Tag                 | Follows                                                         |
|---------------------|-----------------------------------------------------------------|
| `X.Y.Z`, `X.Y`, `X` | A release, or the newest release of that minor or major version |
| `latest`            | The newest release                                              |

It runs as UID 65532 with a read-only root filesystem, reads configuration from `/etc/mcp-compendium` and writes state
to `/var/lib/mcp-compendium`. It has no shell, so health checks and `podman exec` must run `/usr/local/bin/mcpcd`
directly, in exec form.

### Stdio servers

Stdio servers run in the daemon's container, so their runtime has to be there too. Build an image on the runtime your
servers need and copy `mcpcd` into it; which runtime, and keeping it patched, is then yours to choose. Pin exact versions
so rebuilds are reproducible, and start `mcpcd` under an init such as tini, which reaps the processes that stdio
servers leave behind:

```dockerfile
# x-release-please-start-version
FROM ghcr.io/steelshot/mcp-compendium/distroless:0.3.4 AS mcpcd
# x-release-please-end

FROM docker.io/library/node:24-alpine
RUN apk add --no-cache tini && \
    npm install --global --omit=dev --no-audit --no-fund @modelcontextprotocol/server-filesystem@2.2.0 && \
    npm cache clean --force
COPY --from=mcpcd /usr/local/bin/mcpcd /usr/local/bin/mcpcd
COPY --from=mcpcd /etc/mcp-compendium /etc/mcp-compendium
COPY --from=mcpcd --chown=65532:65532 /var/lib/mcp-compendium /var/lib/mcp-compendium
ENV MCP_COMPENDIUM_LISTEN=:8080 \
    MCP_COMPENDIUM_CONFIG=/etc/mcp-compendium \
    MCP_COMPENDIUM_STATE_DIR=/var/lib/mcp-compendium
USER 65532:65532
EXPOSE 8080
VOLUME /var/lib/mcp-compendium
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/mcpcd"]
CMD ["serve"]
```

### Building the image

The [`Containerfile`](../Containerfile) builds the image from a published release rather than from source: it downloads
the release archive for the target architecture and verifies its checksums, and the build context is empty (see
[`.containerignore`](../.containerignore)). `VERSION` selects the release and `RELEASES` a mirror of the release
downloads:

<!-- x-release-please-start-version -->
```bash
podman build --format oci --platform linux/amd64,linux/arm64 --manifest localhost/mcp-compendium:0.3.4 \
  --build-arg VERSION=0.3.4 .
```
<!-- x-release-please-end -->

The same pattern suits air-gapped or secret-dependent builds: serve the archives from an internal `RELEASES` URL, or
mount configuration and secrets into the published image at runtime.

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
and no Kubernetes API access. Configuration changes roll the pod. `image.repository` selects an image built for stdio
servers. The image tag defaults to the chart's version. See
[`values.yaml`](../helm/values.yaml) for every option.

Some managed clusters publish a public OIDC issuer for service account tokens; use it as `issuer`, and neither `jwks`
nor `serviceAccount.automount` is then needed.

## Podman

A rootless [Quadlet](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) unit runs the daemon as a
user service: [`deploy/podman/mcp-compendium.container`](../deploy/podman/mcp-compendium.container), with an example
configuration, [`deploy/podman/config.yaml`](../deploy/podman/config.yaml), that serves GitHub's MCP server to agents on
this host. Comments in both explain what to change. Quadlet makes the unit wait until the network is online (Podman 5.3
or later); `network-online.target` itself has no effect in user units, and the health check must stay in exec form, as
the image has no shell. Install both from the release you run:

```bash
# x-release-please-start-version
version=0.3.4
# x-release-please-end
base="https://raw.githubusercontent.com/steelshot/mcp-compendium/v${version}/deploy/podman"
mkdir -p ~/.config/containers/systemd ~/.config/mcp-compendium
curl -fsSL "${base}/mcp-compendium.container" -o ~/.config/containers/systemd/mcp-compendium.container
curl -fsSL "${base}/config.yaml" -o ~/.config/mcp-compendium/config.yaml
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
