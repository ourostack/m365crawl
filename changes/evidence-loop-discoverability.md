### Added

- `application_evidence_coverage` has `posted_by_app`: how many returned Teams messages name an `app_id` in their served evidence. `mapped` only says the claims were read. Text output prints it as `posted by an app (names an app id): K of N`.
- `--fields app_summary` on `messages`, `unread`, `thread` and `search` gives each message a compact `app_summary` (`state`, `app_name`, `app_id`, `reply_to_id`, `citations_count`) instead of the whole `app_observation`.
- Each entry of `app_observation.served.losses` has a `meaning`, and the help of `messages`, `unread`, `thread`, `search` and `calendar event` ends with every loss code and failure code and its one-line meaning. A partial or failed message repeats the meaning of its own codes.
- `sql --help` lists every table of the archive with what it holds, shows how to list a table's columns (`pragma_table_info()`), and says that `raw_digest` is the SHA-256 of the archived raw JSON as lowercase hex.
- The global `--help` says what `--format log` prints: one `label={json}` line per result.
- `search` reports, in `sources`, what each source matched before `--limit`: `returned` and either `matched` (exact) or `matched_at_least` (a source that stopped reading at the limit). Text output prints a line such as `3 Loop hits not shown; use --source loop` for a source crowded out by newer hits, also when it returned nothing.
- Loop channel items have `latest_cached_snapshot`, the plain inverse of `retained`. Loop sources have `retry_needed`, true on a failed, cancelled or metadata-only source.
- The overview's Loop row has `last_projected_at` and a note that says its time is the last projection, not an edit time, that channels are partial snapshots, and what an archived record is.
- `loop list`, `loop search`, `loop show`, `loop sources` and `search --source loop` fill `archive_age_seconds` with the age of the last complete sync, and text output prints it.

### Changed

- Text output of application evidence is in plain words: `citations: N read from the message, M shown; K more could not be read` replaces "admitted shown" and "source rows refused", and the `full record` line says it is the stored projection as a JSON string, not the raw message. The `[app: NAME]` marker is no longer clipped with the text column.
- The help of the message commands says that `full_inspection` prints the stored projection (a JSON string to decode a second time, in a row that is never updated or deleted), that a citation's `link` is the stored value and is never derived at read time, and what `total_admitted`, `citations_count` and `refused_citation_rows` count.
- Text output of Loop rows no longer says "current": `loop list` has a `text_state` column and a `snapshot` column that says `latest cached snapshot` or `older cached snapshot`, and a row whose record left the app's cache says `archived record gone from the app's cache`. Loop rows of text `search` show `partial_snapshot`. `loop sources` text shows the retry command only on a source that needs it.
- The help of `loop` and `loop sync` says what `loop sync` can and cannot recover when the archived record is gone from the app's cache, and the help of `loop show` says it returns one channel of one snapshot instead of promising "snapshot versions".
- "Start here" and the top-level help describe `search` by every source it searches. The overview's Loop row says `Loop channels (partial snapshots)` and marks its time `(last projection)`.
