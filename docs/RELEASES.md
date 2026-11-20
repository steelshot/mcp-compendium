# Releases

Versions follow [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). They cover the `mcpcd` command
line, its environment variables, the configuration file schema, the agent-facing MCP tools, the admin API and the Helm
chart values; before 1.0.0, a minor release may change any of them.

[release-please](https://github.com/googleapis/release-please) keeps a release pull request open whenever `main` has
unreleased `feat` or `fix` changes, with the next version and its [changelog](../CHANGELOG.md) entries. Merging it tags
the version and publishes the release, then GoReleaser attaches the binaries, and the image and Helm chart follow, all
signed and attested.

A maintainer merges the release pull request, except when a patch release fixes a vulnerability: it then merges itself
once checks pass. The release workflow runs govulncheck on the last release's source with the Go version it was built
with, and on `main` with its own; a vulnerability in code `mcpcd` calls that the last release has and `main` does not
counts. That covers Go modules and Go itself. The check runs when `main` changes and daily, for advisories published
after their fix merged, and only when a patch release pull request is open with changes to Go code or modules.

Renovate titles Go module security fixes, Go patch releases and base image updates `fix(deps):` and merges them once
checks pass, so they reach the next release. Other dependency updates (`chore(deps):`, `ci(deps):`) are merged weekly
without a release.
