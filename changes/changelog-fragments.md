### Changed

- Contributors now add a changelog fragment, `changes/<short-slug>.md`, instead of editing `CHANGELOG.md`, so pull requests that land at the same time no longer conflict. A release assembles the fragments into its version section with `scripts/changelog-assemble.sh`, and CI refuses a pull request that adds lines under `[Unreleased]`.
