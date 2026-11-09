# Contributing

Thank you for helping. Bug reports and feature requests go to
[GitHub issues](https://github.com/steelshot/mcp-compendium/issues); for a larger change, open an issue first so the
approach can be agreed before you write it. Report security vulnerabilities privately as described in
[SECURITY.md](SECURITY.md), never in a public issue or pull request.

Contributions are accepted under the project's licence, [AGPL-3.0-only](LICENSE.md). The project follows the
[REUSE](https://reuse.software/) specification: every Go file starts with a header naming its copyright holder and an
SPDX licence identifier tag for `AGPL-3.0-only`, and [`REUSE.toml`](REUSE.toml) covers files that cannot carry one,
such as configuration, documentation and generated code. A new file outside the paths `REUSE.toml` lists needs a
header; `reuse lint` checks this.

## Development

Requirements: Go (see `go.mod`), golangci-lint v2, Helm with the
[unittest plugin](https://github.com/helm-unittest/helm-unittest), kubeconform, [reuse](https://reuse.software/), and
Podman for image builds.

```bash
go test -race ./...                   # includes the end-to-end and agent evaluation tests, and the fuzz seed corpora
go test ./internal/config -run '^$' -fuzz '^FuzzDecode$' -fuzztime 1m   # fuzz one target
golangci-lint run ./...
go generate ./...                     # regenerate the admin API from openapi.yaml with ogen
helm lint --strict helm && helm unittest helm
helm template ci helm | kubeconform -strict -summary   # add --values helm/ci/<set>-values.yaml per value set
reuse lint                            # licence information for every file
```

`go.mod` requires direct dependencies at their latest versions and indirect ones at the minimum `go mod tidy`
selects. A vulnerable indirect module is overridden with a `replace` directive to a fixed version, which Renovate keeps
current; remove the directive once the required version reaches it.

Code is ordered within each file as types, constants, variables, exported functions, exported methods, unexported
functions and unexported methods; decorder and funcorder enforce it. Package settings follow one convention: each
package declares `Str*` (key and flag), `Env*` and `Def*` constants in `settings.go` and turns them into a typed
`Settings` value, so no other code reads flags or the environment.

### Measuring agent efficiency

[`internal/agenteval`](internal/agenteval) replays scripted agent tasks against a fixture of realistic servers and
compares round trips, estimated tokens and search ranking with a committed baseline, so changes to descriptions,
search or result shaping cannot quietly make agents slower or more expensive:

```bash
go test ./internal/agenteval -v          # report
go test ./internal/agenteval -update     # accept the current results as the new baseline
```

## Pull requests

Pull requests are squash-merged into a single commit whose message is the pull request's **title** alone.
[release-please](https://github.com/googleapis/release-please) reads those titles to choose the next version and
write the changelog, so the title follows a fixed format. The **description** is for review and stays on the pull
request, so it can be as long as the change needs.

### Title

Titles follow [Conventional Commits](https://www.conventionalcommits.org/), and a check enforces it:

```text
<type>[(<scope>)][!]: <description>
```

| Type                                               | For                                            | Release             |
|----------------------------------------------------|------------------------------------------------|---------------------|
| `feat`                                             | A new feature                                  | minor               |
| `fix`                                              | A bug fix                                      | patch               |
| `perf`                                             | A performance improvement                      | in the next release |
| `revert`                                           | Reverting an earlier change                    | in the next release |
| `docs`, `refactor`, `test`, `build`, `ci`, `chore` | Everything that does not change what users run | none                |

- The description starts with a lower-case letter, does not end with a full stop, and is at most 64 characters, so
  the whole title fits a 72-character commit subject. Write it for users: `feat` and `fix` titles become changelog
  entries.
- A scope is optional, for example `fix(oauth): refresh tokens before they expire`.
- `!` after the type or scope marks a breaking change: `feat(config)!: rename the servers map to backends`.

### Description

The template asks for a summary, related issues, the notable changes, breaking changes and how the change was tested,
followed by a short checklist. Its comments explain each section and are hidden once the pull request is opened.
`Fixes #123` in the description closes the issue when the pull request merges.

A breaking change is marked with `!` in the title, which release-please lists under breaking changes in the release
notes; describe what breaks and how to upgrade in the template's *Breaking changes* section.

### Tests and checks

New behaviour and bug fixes come with tests that cover them. The checks run only for the areas a pull request
touches:

- **Go code**: golangci-lint, tests on Linux (x86_64 and arm64), Windows and macOS, generated code and `go.mod`
  tidiness, govulncheck, and a cross-compiled snapshot release.
- **`images/`**: both images, built from the pull request's binaries and smoke-tested.
- **`helm/`**: chart-testing lint, helm-unittest, kubeconform schema validation, and an installation with `helm test` in a
  kind cluster, for each `helm/ci/*-values.yaml`.
- **Always**: workflows are linted with actionlint and zizmor, and every file's licence information with
  `reuse lint`.

"All checks" and "PR title" must pass before a pull request can merge.
