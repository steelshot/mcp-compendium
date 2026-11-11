# Security policy

## Supported versions

Security fixes are released for the newest minor version only. Container images tagged `latest` always point to the
newest release.

## Reporting a vulnerability

Report vulnerabilities privately through
[GitHub's private vulnerability reporting](https://github.com/steelshot/mcp-compendium/security/advisories/new).
Do not open a public issue.

Include the affected version, how to reproduce the problem, and its impact. You will get an acknowledgement within
seven days. Disclosure is coordinated through the advisory: please keep the vulnerability private until a fixed
release is published, normally within 90 days of the report. Fixes are released as patch versions and announced in
the release notes and a GitHub security advisory that credits the reporter unless they prefer not.

## Verifying releases

Release checksums, container images and the Helm chart are signed with Cosign (keyless, from this repository's GitHub
Actions); archives, images and the chart carry build provenance, and images SBOM attestations too. Each release also
includes its provenance as `mcp-compendium-<version>.intoto.jsonl` and the signature of `checksums.txt` as
`checksums.txt.sigstore.json`, for offline verification:

<!-- x-release-please-start-version -->
```bash
version=0.3.3
gh attestation verify "mcp-compendium-${version}-linux-x86_64.tar.xz" --repo steelshot/mcp-compendium
gh attestation verify "mcp-compendium-${version}-linux-x86_64.tar.xz" --repo steelshot/mcp-compendium \
  --bundle "mcp-compendium-${version}.intoto.jsonl"
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/steelshot/mcp-compendium/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify "ghcr.io/steelshot/mcp-compendium/distroless:${version}" \
  --certificate-identity-regexp '^https://github.com/steelshot/mcp-compendium/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
<!-- x-release-please-end -->
