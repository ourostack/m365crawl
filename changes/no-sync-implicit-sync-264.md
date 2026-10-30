### Added

- `--no-sync` (also `M365CRAWL_NO_SYNC=1`) reads the archive as it is: a read never syncs first, whatever `--max-age` says. The flag, including `--no-sync=false`, and an explicit `--max-age` beat the variable. Explicit `sync`, `fetch`, `capture` and `signin` commands are unaffected.
- Every sync-backed read result carries `implicit_sync`: `ran`, `skipped_fresh` (the archive was younger than `--max-age`) or `disabled` (`--no-sync` or `--max-age 0`), beside `archive_age_seconds`. Notes, docs, Loop and native people reads say `implicit_sync: "disabled"` with a notice when `--no-sync` or a zero max age skipped their registered-source refresh.

### Changed

- Emitted follow-up reads (`full_inspection`, `full_text_sql`/`full_text_command`, native attendee routes, retries that read) pin `--no-sync` instead of `--max-age 0`; emitted explicit sync, fetch, capture and signin commands carry no freshness flag. Help, the agent guide (`m365crawl skill`) and the docs teach `--no-sync`. Migration: use `--no-sync` where you meant "do not sync"; for fresh data run `m365crawl sync` first or keep a positive `--max-age`.

### Fixed

- A read that skipped its implicit sync no longer looks fresh. `--max-age 0` keeps its 1.x meaning (no implicit sync), but its results now say `implicit_sync: "disabled"` and add a notice that it turned the sync off rather than asking for fresh data; `--no-sync` adds the notice when the archive is older than 15m. Text mode also prints a `hint:` line on stderr (#264).
