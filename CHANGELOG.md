# Changelog

All notable changes to teamscrawl are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/) with the 0.x caveat that the JSON contract in [SPEC.md](SPEC.md) is stable except where an entry below says "Breaking".

## [Unreleased]

### Added

- `metadata`: prints the crawlkit app manifest (`control.Manifest`) as JSON in every output mode, so `crawlctl discover --app teamscrawl` finds teamscrawl. It needs no archive or Teams cache and never syncs. No schema or existing field changed.

## [0.1.0] - 2026-10-01

The first non-alpha release. Everything below is relative to 0.1.0-alpha.1. No JSON field was renamed or removed and the exit codes did not change, so scripts written against the alpha keep working, with the text changes noted under Changed.

### Added

- Implicit sync notice: a read that runs the `--max-age` sync first prints one line on stderr before it starts and never on stdout: plain in text mode (`teamscrawl: syncing — archive is 2h14m old (max-age 15m)`, or `… no complete sync yet …`), one JSON line in json and log mode (`{"notice":"syncing","reason":"stale"|"never_synced","archive_age_seconds":N,"max_age_seconds":N}`), and its result gains `synced: {seconds, status}` beside `archive_age_seconds`. Reads of a fresh archive stay silent and carry no `synced`.
- `mention_kind` on message items that mention you (`person`, `channel`, `team`, `tag`, `everyone`, or `other` for a kind this version does not know), and `--direct-mentions` on `messages`, `search` and `activity` to keep only `person` mentions, so @channel and @team broadcasts do not drown "who mentioned me".
- `actor_id` and `actor_name` on activity items: who reacted, replied, mentioned or posted (a reaction's actor is a best-effort match by time and carries `actor_inferred: true`), distinct from `sender_*` (the related message's author). `msGraph`, `teamMembershipChange` and `threadActivity` have no actor and omit them.
- `teams`: lists teams `{team_id, display_name, channel_count, last_activity_at, unread_count}` (with `tenant_id`, `user_id`), honoring `--account`, `--limit` and `--fields`. The `--team` usage error now points at it.
- `unread --since <duration|time>` counts only unread messages sent at or after the cutoff, with `--by-conversation`, `--team` and `--include-channels` too: the recency cap for "what needs my attention", since old read markers leave conversations with hundreds of unread messages.
- `watch [--every 60s] [--emit-initial]`: streams one JSON line per new, edited or deleted message or activity item as Teams writes its cache (file events with a poll as the safety net, debounced), plus one `sync` line per sync and one `migrated` line when an upgrade re-derives an older archive. It honors `--account`, `--fields` and `--max-text`, skips system pseudo-conversations and exits 0 on SIGINT or SIGTERM.
- `--team <name|id>` on `conversations`, `messages`, `search`, `unread` and `activity`: limits results to one team and its channels. An unknown or ambiguous name is a `usage` error that lists the matches.
- `total` on truncated list results and on truncated `sql` results: the exact number of matches ignoring `--limit`. It is omitted when the result is not truncated.
- `reply_count` and `last_reply_at` on channel thread roots in `messages`, `search`, `unread` and `thread`.
- `search` works with filters alone (`--mentions-me`, `--from`, `--conversation`, `--team`, `--since`, `--until`). With nothing at all it is a `usage` error that points at `messages`.
- `activity --type` takes a comma-separated list, and `--help` and the skill document every activity type seen in a real cache.
- `conversations --query` ranks an exact name first, then a prefix, then a substring, then word matches.
- Archive upgrade: `meta.derivation_version` and the `migrated` report field (`{from, to, rows}`). See Changed.
- `skill`: prints the agent guide, embedded in the binary, as raw Markdown in every output mode (an exception to the JSON default, like `--help`), so the guide always matches the installed version.
- `version` prints `{"version", "commit", "date"}` as JSON (a human line in text mode), and `teamscrawl --version` does the same.
- Partial syncs: each Teams source commits on its own. When one fails and another succeeds, `sync` reports status `partial` with a result per source (`accounts`, `counts` or `error`), prints the report on stdout and a new `partial_sync` error (exit 1) on stderr. `watch` emits the committed sources' changes, then an error line, and retries only the failed sources.
- `doctor` checks `archive_newer` (fails when a newer teamscrawl wrote the archive), `archive_upgrade` (warns that the next sync upgrades an older archive) and `last_sync_status` (warns after a partial or failed sync).
- A second Ctrl-C (or SIGTERM) during a stop quits at once with exit 130.
- The release workflow runs `make check` before it publishes.
- Error codes `interrupted` (exit 1; a command stopped by SIGINT or SIGTERM, which alpha.1 reported as `internal`) and `archive_newer` (exit 3; an archive written by a newer teamscrawl is refused for writing before anything is written, and read commands keep working).
- Real-cache acceptance tests (`make acceptance`, local only): a differential test of the native V8 decoder against Node's `v8.deserialize` for every allowlisted record, volume comparison against an independent Python reader, blob resolution, recency, and a check that the auth databases are never decoded.
- End-to-end tests that run the built binary against the committed fixture (`make e2e`), including the lock, interruption and environment error paths.
- A 100% per-function statement coverage gate over `internal/...` (`make coverage`, required in CI), with a shrinking per-package allow-list.
- Developer ID signing and notarization of the darwin binaries in the release pipeline, when the Apple signing secrets are available.
- `SPEC.md` (the normative specification), `docs/commands.md`, `docs/how-it-works.md`, `docs/full-disk-access.md` and this changelog.

### Changed

- Bot, card and call items read as text. A bot's Adaptive Card, hero or connector card gives its title, text blocks and facts as `text`; call and meeting events read `Call ended · 23m`; thread events read `Member added`; call recording and transcript notices read `Call transcript available` instead of raw JSON. `raw_json` is unchanged. Call events show a sender, taken from the people table when the message carries no name. A script that tested for empty `text` on these items will now see text.
- Untitled group chats are named after their other members (`Ana, Ben, Chao +2`), or `Unnamed chat (5 members, id 3fa9c2d1)` when no member name is known.
- An archive made by alpha.1 is upgraded in place. The first `sync` (or `watch`, or implicit sync) re-derives message text, sender names and untitled-chat names from each row's stored `raw_json`, including rows Teams has since evicted from its cache, rebuilds the text indexes and records derivation version 2. The rows count as no update and no edit. Read-only commands do not migrate. The sync can report `status: unchanged` and `migrated` together.
- Peak memory during a full sync fell from about 0.8 GB to about 0.2 GB on a real cache: the reader keeps values only for the allowlisted databases and re-reads values of 512 bytes or more from the snapshot on demand. The reader decodes LevelDB tables with its own block reader instead of goleveldb's.
- `sync` applies each source in one transaction in batches of 2,000 records, so memory stays bounded and a failed source rolls back completely.
- A relative `--db` path is resolved against the current directory.
- `archive_age_seconds` counts from the last fully successful sync of the accounts the read covers (`--account`, or the stalest account); a partial or failed sync refreshes nobody. `needs_sync` also appears when no complete sync exists yet, including an archive from an older version that has not been upgraded. In JSON mode the plain-text `hint:` line on stderr is gone (the result carries `hint`); text mode still prints it.
- `sql` streams rows and stops at `--limit`: `truncated` is still set, but `total` is no longer reported. A word such as `attach` inside a string literal, comment or quoted name no longer makes a query a usage error.
- `whoami`'s nested `archive.archive_age_seconds` equals the top-level one.

### Fixed

- A relative `--db` no longer fails with `invalid uri authority`.
- An interrupted command reports `interrupted` instead of `internal`.

### Security

- Documented that the archive holds message content verbatim, including anything people pasted (links with tokens included), and that the archive and its lock file are mode 0600 in a 0700 directory (SPEC.md section 8).

## [0.1.0-alpha.1] - 2026-09-30

First public alpha.

### Added

- `doctor`, `whoami`, `sync`, `status`, `search`, `messages`, `thread`, `unread`, `activity`, `conversations`, `people` and a read-only `sql`.
- A native reader for the new Teams desktop cache: a manifest-driven LevelDB reader that does not depend on the IndexedDB key comparator, Chromium's IndexedDB coding, the Blink value envelope (snappy, blobs, the version 21 trailer) and a Go V8 structured-clone deserializer for wire versions 13 to 16. No Node, Python or browser is needed at runtime.
- A SQLite archive (WAL) with FTS5 search, idempotent upserts, sticky `deleted_at` and a keep-forever rule for messages Teams evicts from its cache. Only the `replychain-manager`, `conversation-manager` and `activity-manager` databases are decoded; the auth database never is.
- An agent-first JSON contract: one document per command on stdout, stable snake_case keys, lists as `{items, count, truncated}`, errors as `{error: {code, message, fix}}` on stderr, exit codes 0 to 4, `archive_age_seconds` on every read result, `--max-age` implicit sync with a `sync_error` fallback, `--fields`, `--max-text`, and a Teams deep `link` on every item.
- Unread state (`unread`, `--by-conversation`, `--include-channels`, `channels_excluded`) and exact `mentions_me`.
- A terminal presentation for text mode, a README that argues the tool, the agent skill, a Homebrew cask, and release builds for darwin amd64 and arm64.

### Known issues

- Peak memory of about 1 GB during a full sync.
- Bot, call and some activity items could have an empty sender or empty text, or raw card JSON as text.
- No reply counts on thread roots, no `--team` filter and no `watch`.
- Unsigned binaries.

[0.1.0]: https://github.com/ourostack/teamscrawl/compare/v0.1.0-alpha.1...v0.1.0
[0.1.0-alpha.1]: https://github.com/ourostack/teamscrawl/releases/tag/v0.1.0-alpha.1
