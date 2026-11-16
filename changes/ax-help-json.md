### Added

- `m365crawl help --json` prints every command with who sees its effect (`private` or `others`), whether it only reads, and the exit codes the CLI gives.

### Fixed

- `m365crawl edit`, `delete` and `remove` change nothing and name the command that does the real change.
- A closed pipe (`m365crawl --help | head`) ends with a usage status instead of being killed by a signal.
