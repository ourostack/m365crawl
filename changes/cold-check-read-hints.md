### Added

- `--fields app_summary` adds `losses_total`: the sum of the counts in `app_observation.served.losses`, null when nothing is served. The text cell adds `N native fields not read` when it is above zero.
- Text `search` names each source whose match count is a floor because it stopped reading at `--limit` (`matched_at_least` in JSON), and `search --help` says the count is a floor, not a total.
- Text `loop list` and `loop search` print `partial snapshots of cached text, not full or current pages` under a table that returned rows.
- The help of `--since` and `--until` on `docs list` and `docs search` says which rows they never match.
