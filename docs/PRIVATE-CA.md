# Private certificate authorities

Servers behind a private or corporate certificate authority fail with x509 errors until it is trusted; `mcpcd status`
shows the error in the server's last error. `mcpcd` verifies `http` and `sse` servers itself, but a stdio server makes
its own connections with its own TLS library, which has to trust the CA too.

## How mcpcd finds certificate authorities

On Linux and in the images, `mcpcd` reads one bundle file (`/etc/ssl/certs/ca-certificates.crt` in the images, or the
file `SSL_CERT_FILE` names) and then every PEM file in the directories listed in `SSL_CERT_DIR`. A directory holding
only the CA therefore adds it without replacing the bundle.

On macOS and Windows, `mcpcd` uses the system trust store: add the CA there. Setting `SSL_CERT_FILE` or `SSL_CERT_DIR`
replaces the store with files on disk; when a stdio server needs those variables, set
`GODEBUG=x509sslcertoverrideplatform=0` for `mcpcd` to keep the store.

## Remote servers only

The distroless image runs no stdio servers, so trusting the CA in `mcpcd` is enough. With Podman, put the CA in a
directory of its own and add to the [unit](DEPLOYMENT.md#podman)'s `[Container]` section:

```ini
Volume=%h/.config/mcp-compendium-ca:/etc/ssl/private-ca:ro,Z
Environment=SSL_CERT_DIR=/etc/ssl/private-ca
```

To trust exactly what the host trusts instead, mount the host's bundle file over the image's, for example
`Volume=/etc/pki/tls/certs/ca-bundle.crt:/etc/ssl/certs/ca-certificates.crt:ro` on Fedora and RHEL
(`/etc/ssl/certs/ca-certificates.crt` on Debian and Ubuntu, `/etc/ssl/ca-bundle.pem` on openSUSE). Mount the file, not
the host's `/etc/ssl/certs` directory: its entries are often symlinks that dangle inside the container. The host's CA
tools replace the bundle file when its CAs change, and the mount keeps showing the old one, so restart the unit
afterwards (`systemctl --user restart mcp-compendium`).

With the Helm chart, put the CA in a ConfigMap and mount it:

```yaml
extraVolumes:
  - name: private-ca
    configMap: { name: corporate-ca }
extraVolumeMounts:
  - name: private-ca
    mountPath: /etc/ssl/private-ca
    readOnly: true
env:
  - name: SSL_CERT_DIR
    value: /etc/ssl/private-ca
```

## Stdio servers

Stdio servers inherit `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS` and `NODE_USE_SYSTEM_CA` from the daemon;
pass any other variable through the server's `env`. Each TLS library uses them differently:

| Client                                     | Trusts by default | Private CA                                                                                                     |
|--------------------------------------------|-------------------|----------------------------------------------------------------------------------------------------------------|
| Go                                         | The system bundle | As `mcpcd`                                                                                                     |
| OpenSSL: Python's `ssl` and `urllib`, curl | The system bundle | `SSL_CERT_FILE` replaces the bundle; `SSL_CERT_DIR` replaces the directory, read only through hash-named links |
| httpx, used by the MCP Python SDK          | certifi's bundle  | `SSL_CERT_FILE`, or else `SSL_CERT_DIR`, replaces it entirely                                                  |
| Requests                                   | certifi's bundle  | `REQUESTS_CA_BUNDLE`, or else `CURL_CA_BUNDLE`, replaces it; set it in the server's `env`                      |
| Node.js                                    | Its own CA list   | `NODE_EXTRA_CA_CERTS` adds a file; `NODE_USE_SYSTEM_CA=1` adds the system bundle, from Node.js 22.19 or 24.6   |

A directory holding only the CA, as for remote servers, would leave httpx-based servers trusting nothing else. Give
stdio servers one bundle with both the public CAs and the corporate one instead: add the CA to Alpine's bundle in an
image derived from `minimal`, and name that bundle so that certifi-based clients use it too. `mcpcd` reads the same
bundle.

```dockerfile
# x-release-please-start-version
FROM ghcr.io/steelshot/mcp-compendium/minimal:0.3.2
# x-release-please-end
USER root
COPY corporate.pem /usr/local/share/ca-certificates/corporate.crt
RUN apk add --no-cache ca-certificates && update-ca-certificates
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    NODE_USE_SYSTEM_CA=1
USER 65532:65532
```

Alpine's Node.js supports `NODE_USE_SYSTEM_CA`; for an older Node.js, set
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` instead. A server using Requests also needs the bundle in its
`env`:

```yaml
servers:
  tracker:
    type: stdio
    command: tracker-mcp-server
    env:
      REQUESTS_CA_BUNDLE: /etc/ssl/certs/ca-certificates.crt
```

The same variables work without building the CA in, with the host's bundle mounted over the image's as shown for
remote servers.
