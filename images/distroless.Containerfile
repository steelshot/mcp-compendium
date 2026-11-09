# Distroless image: the mcpcd binary and CA certificates on an empty base, for remote (http and sse) MCP servers.
#
#   podman build -f images/distroless.Containerfile --build-arg VERSION=0.1.0 -t mcp-compendium/distroless images
#
# VERSION is a release version without the leading v. RELEASES may point at a mirror of the release downloads.

# Fetch stage: download the release archive, verify it against the release checksums and extract the binary.
FROM docker.io/library/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS fetch

ARG VERSION
ARG TARGETARCH
ARG RELEASES=https://github.com/steelshot/mcp-compendium/releases/download

WORKDIR /tmp

RUN set -eu; \
    test -n "${VERSION}" || { echo "set --build-arg VERSION to a release version" >&2; exit 1; }; \
    case "${TARGETARCH}" in \
        amd64) arch=x86_64 ;; \
        arm64) arch=arm64 ;; \
        *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    echo "mcp-compendium-${VERSION}-linux-${arch}.tar.xz" > archive-name

RUN set -eu; \
    archive="$(cat archive-name)"; \
    wget -q -O "${archive}" "${RELEASES}/v${VERSION}/${archive}"; \
    wget -q -O checksums.txt "${RELEASES}/v${VERSION}/checksums.txt"

RUN set -eu; \
    archive="$(cat archive-name)"; \
    awk -v name="${archive}" '$2 == name' checksums.txt > expected.txt; \
    test -s expected.txt; \
    sha256sum -c expected.txt

RUN set -eu; \
    mkdir -p /out; \
    tar -xJf "$(cat archive-name)" -C /out mcpcd

RUN set -eu; \
    mkdir -p /rootfs/etc/mcp-compendium /rootfs/var/lib/mcp-compendium /rootfs/tmp; \
    chmod 1777 /rootfs/tmp; \
    echo 'mcpcd:x:65532:65532:mcpcd:/var/lib/mcp-compendium:/sbin/nologin' > /rootfs/passwd; \
    echo 'mcpcd:x:65532:' > /rootfs/group


# Runtime stage.
FROM scratch

ARG VERSION

COPY --from=fetch /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=fetch /rootfs/passwd /etc/passwd
COPY --from=fetch /rootfs/group /etc/group
COPY --from=fetch /rootfs/etc/mcp-compendium /etc/mcp-compendium
COPY --from=fetch --chown=65532:65532 /rootfs/var/lib/mcp-compendium /var/lib/mcp-compendium
COPY --from=fetch /rootfs/tmp /tmp
COPY --from=fetch /out/mcpcd /usr/local/bin/mcpcd

ENV MCP_COMPENDIUM_LISTEN=:8080 \
    MCP_COMPENDIUM_CONFIG=/etc/mcp-compendium \
    MCP_COMPENDIUM_STATE_DIR=/var/lib/mcp-compendium

USER 65532:65532
EXPOSE 8080
VOLUME /var/lib/mcp-compendium

LABEL org.opencontainers.image.title="mcp-compendium distroless" \
      org.opencontainers.image.description="Serve many MCP servers to AI agents as one compact, searchable endpoint." \
      org.opencontainers.image.source="https://github.com/steelshot/mcp-compendium" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}"

ENTRYPOINT ["/usr/local/bin/mcpcd"]
CMD ["serve"]
