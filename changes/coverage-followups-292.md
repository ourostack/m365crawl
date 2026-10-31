### Added

- `search` bounded by `--since` or `--until` adds `range_coverage` for the chats and mail it searched, like `messages` and `mail list`, so a zero hit can be told from a range the archive does not cover.
- The `calendar` agenda adds `range_coverage`: per principal, whether a calendar cache showed every day of the range, with the principal's events counted by the default filters.
- `messages` and `search` narrowed by `--conversation` or `--team` judge only the conversations they read (`narrowed_to`); a selection the archive does not hold is `not_covered` with reason `conversation_not_archived`.

### Fixed

- A chat route for a range with no start no longer prints a year-one `--since`; it counts over WorkIQ's default window.
