# Private certificate authorities

Servers behind a private or corporate certificate authority fail with x509 errors until it is trusted; `mcpcd status`
shows the error in the server's last error. On macOS and Windows, `mcpcd` uses the system trust store: add the CA
there, and leave `SSL_CERT_FILE` and `SSL_CERT_DIR` unset, as either replaces the store. On Linux and in the images,
`mcpcd` reads the CA bundle (`/etc/ssl/certs/ca-certificates.crt` in the images) and then every PEM file in the
directories listed in `SSL_CERT_DIR`. Mounting the CA into a directory of its own and pointing `SSL_CERT_DIR` at it
therefore adds the CA without replacing the bundle.

Stdio servers inherit `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS` and `NODE_USE_SYSTEM_CA` from the daemon.
OpenSSL-based servers, such as Python's, replace their own trust store with `SSL_CERT_FILE` and `SSL_CERT_DIR` too.
Node.js servers need `NODE_EXTRA_CA_CERTS` naming the CA file; pass other runtimes' variables through the server's
`env`.

With Podman, add to the [unit](DEPLOYMENT.md#podman)'s `[Container]` section:

```ini
Volume=%h/.config/mcp-compendium-ca:/etc/ssl/private-ca:ro,Z
Environment=SSL_CERT_DIR=/etc/ssl/private-ca
Environment=NODE_EXTRA_CA_CERTS=/etc/ssl/private-ca/corporate.pem
```

To trust exactly what the host trusts instead, mount the host's bundle file over the image's, for example
`Volume=/etc/pki/tls/certs/ca-bundle.crt:/etc/ssl/certs/ca-certificates.crt:ro` on Fedora and RHEL
(`/etc/ssl/certs/ca-certificates.crt` on Debian and Ubuntu, `/etc/ssl/ca-bundle.pem` on openSUSE). Mount the file, not
the host's `/etc/ssl/certs` directory: its entries are often symlinks that dangle inside the container.

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

An image derived from `minimal` can add the CA to Alpine's bundle instead, which `mcpcd` reads, as do Node.js servers
when `NODE_USE_SYSTEM_CA=1` is set:

```dockerfile
# x-release-please-start-version
FROM ghcr.io/steelshot/mcp-compendium/minimal:0.3.1
# x-release-please-end
USER root
COPY corporate.pem /usr/local/share/ca-certificates/corporate.crt
RUN apk add --no-cache ca-certificates && update-ca-certificates
USER 65532:65532
```
