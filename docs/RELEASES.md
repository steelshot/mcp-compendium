# Releases

Versions follow [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). They cover the `mcpcd` command
line, its environment variables, the configuration file schema, the agent-facing MCP tools, the admin API and the Helm
chart values; before 1.0.0, a minor release may change any of them.

[release-please](https://github.com/googleapis/release-please) keeps a release pull request open whenever `main` has
unreleased `feat` or `fix` changes, with the next version and its [changelog](../CHANGELOG.md) entries. Merging it tags
the version and publishes the release, then GoReleaser attaches the binaries, and the images and Helm chart follow,
all signed and attested.

A release pull request that only bumps the patch version merges itself once checks pass when a pull request labelled
`security` has merged since the last release, so vulnerability fixes ship straight away; Renovate labels the updates
that fix a known vulnerability in a Go module. Otherwise it merges itself once it has been open for 14 days, which
collects bug fixes, Go patch releases and base image updates (titled `fix(deps):` by Renovate) into one release, unless
a maintainer merges it sooner. Minor and major releases are always merged by a maintainer. Other dependency updates
(`chore(deps):`, `ci(deps):`) are merged weekly without a release.
