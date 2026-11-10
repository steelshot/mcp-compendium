# Minimal image: Alpine with tini and the mcpcd binary. Derive from it to add stdio MCP servers; see docs/DEPLOYMENT.md.
#
#   podman build -f images/minimal.Containerfile --build-arg VERSION=0.1.0 -t mcp-compendium/minimal images
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


# Runtime stage.
FROM docker.io/library/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

ARG VERSION

RUN apk add --no-cache tini

RUN addgroup -S -g 65532 mcpcd && \
    adduser -S -D -H -u 65532 -G mcpcd -h /var/lib/mcp-compendium -s /sbin/nologin mcpcd && \
    install -d -o root -g root -m 0755 /etc/mcp-compendium && \
    install -d -o mcpcd -g mcpcd -m 0700 /var/lib/mcp-compendium

COPY --from=fetch /out/mcpcd /usr/local/bin/mcpcd

ENV MCP_COMPENDIUM_LISTEN=:8080 \
    MCP_COMPENDIUM_CONFIG=/etc/mcp-compendium \
    MCP_COMPENDIUM_STATE_DIR=/var/lib/mcp-compendium

USER 65532:65532
EXPOSE 8080
VOLUME /var/lib/mcp-compendium

LABEL org.opencontainers.image.title="mcp-compendium minimal" \
      org.opencontainers.image.description="Serve many MCP servers to AI agents as one compact, searchable endpoint." \
      org.opencontainers.image.source="https://github.com/steelshot/mcp-compendium" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.base.name="docker.io/library/alpine:3.24"

# tini reaps orphaned grandchildren of stdio servers, which mcpcd as PID 1 would leave as zombies.
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/mcpcd"]
CMD ["serve"]
