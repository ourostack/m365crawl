# Changelog fragments

Each pull request adds one file here, `changes/<short-slug>.md`, instead of editing `CHANGELOG.md`, so pull requests that land at the same time never conflict. CI fails a pull request that adds lines under `## [Unreleased]` in `CHANGELOG.md`.

A fragment holds Keep a Changelog subsections: a `### Added`, `### Changed`, `### Deprecated`, `### Removed`, `### Fixed` or `### Security` line, then `- ` bullets written as they would appear in `CHANGELOG.md`. A line indented with spaces continues the bullet above it.

```markdown
### Fixed

- `transcripts fetch` no longer fails when the browser exits early.
```

A release runs `scripts/changelog-assemble.sh X.Y.Z YYYY-MM-DD`, which merges every fragment into a new `## [X.Y.Z] - YYYY-MM-DD` section and deletes the fragments (see `docs/releasing.md`). `scripts/changelog-assemble.sh --check` validates the fragments without changing anything.
