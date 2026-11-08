<!--
Title: a Conventional Commit, such as "fix(oauth): refresh tokens before they expire". It becomes the squash commit
and the changelog entry; see CONTRIBUTING.md. Mark a breaking change with ! after the type, as in "feat(config)!: ...".

This description is not part of the commit, so make it as thorough as the change needs. These comments are hidden.
-->

## Summary

<!-- What changes and why, from the user's point of view where there is one. -->

## Related issues

<!-- "Fixes #123" closes the issue when this merges; "Refs #123" only links it. Delete the section if there are none. -->

## Changes

<!-- The notable changes, one per line, including any to configuration, the chart or the admin API. -->

## Breaking changes

<!-- What breaks and how to upgrade, or "None". A breaking change also needs ! in the title. -->

None

## Testing

<!-- Tests added or run, and anything checked by hand, such as a local image build or a chart install. -->

## Checklist

- [ ] The title follows Conventional Commits ([CONTRIBUTING.md](https://github.com/steelshot/mcp-compendium/blob/main/CONTRIBUTING.md#title))
- [ ] New behaviour and bug fixes are covered by tests
- [ ] Documentation is updated where needed (README, CONTRIBUTING.md, chart values, `openapi.yaml`)
- [ ] Breaking changes are marked with `!` in the title and described above
