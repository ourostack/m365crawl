# teamscrawl specification

Status: normative for v0.1.0.

This document defines what teamscrawl does and what its output promises. Where this document and the code disagree, that is a bug in one of them and is fixed in the same change that finds it. The words MUST, MUST NOT and SHOULD carry their usual meaning. Command-by-command flag tables live in [docs/commands.md](docs/commands.md); the reading path through the Teams cache lives in [docs/how-it-works.md](docs/how-it-works.md).

## 1. Purpose and scope

teamscrawl lets an agent on a Mac read the operator's Microsoft Teams history quickly and offline. It mirrors the new Teams desktop app's local cache into a local SQLite archive with full-text search, then answers queries from that archive. Its primary users are agents, so it favors predictable commands, stable JSON, coded errors with a `fix`, and self-diagnosis over interactive features.

It never talks to the Teams service, never reads or uses Teams credentials, and never writes to Teams' storage. It has no network code.

In scope for v0.1.0:

- macOS and the new Teams app (`com.microsoft.teams2`), every WebView2 profile, every Teams origin and every signed-in account found in them.
- Conversations (chats, channels, teams, meetings), messages (channel posts, replies, chat and meeting messages), the activity feed, read state, and people derived from message senders and conversation members.
- Commands: `doctor`, `whoami`, `sync`, `status`, `search`, `messages`, `unread`, `activity`, `thread`, `conversations`, `people`, `sql`, `watch`, `version`.

Out of scope: sending, reacting, marking read or any other write to Teams; using Teams tokens; classic Teams; Windows and Linux hosts (CI runs on Linux, but there is no Teams to read there); downloading attachments or media (files and links are stored as metadata); a terminal UI.

## 2. Data sources and the database allowlist

New Teams runs an Edge WebView2 whose user data directory is `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView` (the default `--teams-root`). teamscrawl reads that directory as follows.

- **Profiles.** Every `WV2Profile_*` directory (any directory under the root that has an `IndexedDB` subdirectory).
- **Origins.** In each profile, every directory `IndexedDB/https_teams.microsoft.com_<n>.indexeddb.leveldb` or `https_teams.cloud.microsoft_<n>.indexeddb.leveldb` (with its sibling `.indexeddb.blob` directory). One profile and origin pair is one **source**, identified as `<profile>|<origin>`. Other IndexedDB origins are never read; `status`, `sync` and `doctor` list their names (`other_origins`) so drift is visible.
- **Databases.** Inside an origin, database names follow `Teams:<manager>:react-web-client:<tenantId>:<userId or 8:orgid:userId>:<locale>`. The tenant and user ids of an account come from that name.

teamscrawl MUST decode only the databases in this allowlist and MUST NOT decode, list the contents of or log any other database:

| Manager | Object store | Becomes |
| --- | --- | --- |
| `replychain-manager` | `replychains-2` | messages (each record is a reply chain holding a map of messages) and people |
| `conversation-manager` | `conversations` | conversations and people |
| `activity-manager` | `feed-items` | activity items |

Every other database is skipped by name, above all `Teams:auth:*`, which holds sign-in material. The reader keeps no value of a non-allowlisted database in memory. An allowlisted manager that is absent is skipped; one that is present without its object store fails the sync with `store_missing`.

## 3. The archive

The archive is one SQLite file (WAL mode) at `~/.teamscrawl/teamscrawl.db` by default, overridable with `--db` or `TEAMSCRAWL_DB`. A relative `--db` path is resolved against the current directory. The archive's schema version is 2 (`schema_migrations`, shown as `schema_version` by `status` and `doctor`). Timestamps are stored as UTC text with millisecond precision and printed as RFC3339 UTC.

Rows are partitioned by account: `(tenant_id, user_id)`. Two accounts never mix. The user id is the bare GUID; the account's own sender id is `8:orgid:<user_id>`.

### 3.1 Tables

| Table | Key | Fields agents see |
| --- | --- | --- |
| `accounts` | `(tenant_id, user_id)` | `profile`, `locale`, `first_seen_at`, `last_synced_at` |
| `conversations` | `(tenant_id, user_id, id)` | `kind` (`Chat`, `Topic` for a channel, `Space` for a team, `Meeting`, ...), `title`, `topic`, `display_name` (`Team › Channel` for a channel, the chat title otherwise, member names for an untitled chat), `team_id`, `parent_id`, `members_json`, `last_message_at`, `read_horizon_at`, `read_horizon_client_message_id`, `favorite`, `raw_json` |
| `messages` | `(tenant_id, user_id, conversation_id, id)` | `reply_chain_id`, `parent_message_id`, `client_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `content_type`, `content_html`, `content_text`, `version`, `mentions_json`, `mentions_me`, `reactions_json`, `files_json`, `links_json`, `subject`, `importance`, `pinned`, `link`, `raw_json` |
| `people` | `(tenant_id, id)` | `display_name`, `first_seen_at`, `last_seen_at` |
| `activity` | `(tenant_id, user_id, id)` | `type`, `subtype`, `is_read`, `at`, `conversation_id`, `message_id`, `reply_chain_id`, `app_id`, `raw_json` |
| `sync_runs` | `id` | `started_at`, `finished_at`, `source`, `fingerprint`, `status` (`ok`, `ok_with_omissions`, `unchanged`, `partial`, `failed`), `counts_json`, `omissions_json`, `accounts_json`. A run writes one row per source it tried and one run-level row (empty `source`, `accounts_json` set to the accounts it covered: `["*"]` for every account, or one `<tenantId>/<userId>`). Only run-level rows with a fully successful status count as a fresh sync. |
| `meta` | `key` | `derivation_version` |
| `message_fts`, `conversation_fts` | rowid of the owning row | FTS5 indexes over message text and conversation titles |

Every table also carries internal bookkeeping columns (`content_hash`, `updated_at`) that are not part of the contract. The `sql` command can read every table above. Column names listed here are stable; adding a column is compatible, renaming or removing one is a breaking change recorded in the changelog.

A message's `text` (`content_text`) is plain text derived from the HTML body: tags dropped, entities decoded, an @-mention shown as the person's name, whitespace collapsed. For bot cards (Adaptive, hero and connector cards), call and meeting events, thread events and call recording or transcript notices, `text` is a readable summary (`Call ended · 23m`, `Member added`), never raw JSON. The original record stays in `raw_json`.

### 3.2 Archive semantics

- **The archive outlives Teams' cache.** Teams evicts old messages from its own cache as it runs. teamscrawl MUST NOT delete a message, conversation, person or activity item because it disappeared from the cache. No code path issues a `DELETE` against these tables (it only replaces the full-text rows of a row it updates).
- **Deletions are tombstones.** When Teams marks a message deleted, the row gets `deleted_at`. The row, its text and its `raw_json` remain.
- **`deleted_at` is sticky.** Once set, it stays set when a later sync sees the same or an older version of the message without it. A strictly newer version without a deletion clears it.
- **`--include-deleted` is off by default.** `messages`, `search` and `thread` hide deleted messages unless the flag is given. Deleted messages are never counted as unread.
- **Idempotent upserts.** A message changes only when the incoming `version` is newer, or the version is equal and the content hash differs. An older version is ignored. A second sync with no new Teams activity changes no row.
- **Unread** means: the message is newer than its conversation's `read_horizon_at`, is not deleted and was not sent by the account's own user. The horizon comes from Teams' consumption horizon in the conversation properties.
- **`mentions_me`** is true when the message mentions the account's own user.
- **System pseudo-conversations** `48:notifications`, `48:calllogs` and `48:annotations` are hidden from every list command unless `--include-system` is given, because they duplicate real messages without sender or conversation names. `48:notes` (the user's own notes) is not hidden.
- **Derivation version.** Text, sender names and untitled-chat names are derived from `raw_json` by the mappers. The archive records the mapper generation in `meta.derivation_version`; this build writes 2. An archive with no such row was written by alpha.1 (version 1). The first `sync` on an older archive recomputes the derived fields of every stored row from its `raw_json` (rows Teams has since evicted included), rebuilds their text indexes, and records the new version, all in one transaction before the cache is read. The recomputation counts as no update and no edit. An archive with a higher derivation version than the build's is refused with `archive_newer` before any write.

### 3.3 Full-text index

`message_fts(message_key unindexed, content)` and `conversation_fts(conversation_id unindexed, title)` are FTS5 tables whose rowid equals the rowid of the owning `messages` or `conversations` row. The store maintains them in the same transaction as the upsert. `messages` and `conversations` are therefore rowid tables with no explicit integer primary key, and their rowids are load-bearing: an operation that renumbers them, such as `VACUUM` (SQLite documents that VACUUM may change the rowids of such tables), would silently detach the indexes from their rows. Nothing in teamscrawl runs `VACUUM`, and nothing outside teamscrawl SHOULD run it on an archive. If an archive's indexes are damaged, move it aside and run `teamscrawl sync` to rebuild a fresh archive from the cache.

A `search` query is split on whitespace into terms that are ANDed. `"quoted phrases"` match as phrases and a trailing `*` on a bare term is a prefix. Every FTS5 operator in the input is quoted, so it is an ordinary word.

## 4. Sync

`sync` mirrors every source into the archive once. A run proceeds as follows.

1. **Lock.** The run takes an exclusive, non-blocking `flock` on `<db>.lock` (mode 0600). If another sync or watch sync holds it, the run fails at once with `locked` (exit 4); it never waits. Read commands do not take the lock and work beside a writer.
2. **Sweep.** Snapshot directories named `teamscrawl-snapshot-*` in the temp directory that are older than one hour (left by a killed process) are removed.
3. **Migrate.** An older archive is re-derived as described in section 3.2. An archive that is newer than this build fails with `archive_newer`, and nothing is written, not even a failed-run record.
4. **Discover** the sources (section 2). Errors: `teams_not_installed`, `no_full_disk_access`, `no_teams_origin`.
5. **Per source:** fingerprint, skip or snapshot, decode, map, write.
   - The **fingerprint** is a SHA-256 over the decoder version and the sorted name, size and modification time of every file in the origin's `.leveldb` and `.blob` directories, excluding `LOCK` and `LOG*`. If it equals the fingerprint of the last successful run for that source, the source is `unchanged`, nothing is decoded and no row changes. A run with `--account` records no fingerprint and never skips, so a filtered run cannot hide another account's data from a later run.
   - The **snapshot** copies the two directories into a private temp directory (mode 0700, files 0600), regular files only. LevelDB data files are copied first, then the `MANIFEST`, then `CURRENT`; blobs last. The copy is retried, up to three attempts, when `CURRENT` changed during the copy, the copied manifest is not the size of the live one, or a file the manifest names is missing. After three attempts the run fails with `snapshot_inconsistent`. The snapshot is validated by reading every table and log. It contains Teams' sign-in database, so it is removed when the source is done, on failure, and on SIGINT or SIGTERM.
   - **Decode and map** read only the allowlisted stores of the snapshot (section 2).
6. **Write.** Each source is applied in **one write transaction**: accounts, conversations, messages, activity items, people and the source's `sync_runs` row commit together or not at all. Records are applied in batches of 2,000 so memory stays bounded. People are merged across the whole source; the newest sighting names a person. Sources are independent: a source that fails (including from a panic, which becomes `internal`) rolls back completely, is recorded as `failed`, and does not stop the sources after it. Sources that committed stay committed.
7. **Record and report.** The run writes its run-level `sync_runs` row (section 3.1) and prints its report (section 5, `sync`). Only a run in which every source succeeded counts as a fresh sync, for every account (or for the one account of `sync --account`). A partial or failed run refreshes no account.

**Consistency.** Teams keeps writing while teamscrawl copies. The copy order, the retry and the validation above give a self-consistent copy or a `snapshot_inconsistent` error. A truncated tail in the active log file (Teams was mid-write) is tolerated and counted as the omission `truncated_log_tail`. Because each source is one transaction, a reader never sees half a source. A source that fails leaves the archive as it was for that source, and the next sync retries it.

**Omissions.** A record teamscrawl cannot decode or map is skipped, counted by reason in `omissions`, and never guessed. A sync with at least one omission has status `ok_with_omissions` and still exits 0.

| Reason code | Meaning |
| --- | --- |
| `empty_value` | The record has no value (Teams leaves some records empty). |
| `unknown_envelope` | The record's Blink envelope is not recognized. The detail ends with the first 16 bytes in hex. |
| `blob_missing` | The record's value is stored in an external blob file and the blob entry or file is absent. |
| `bad_key` | The record's IndexedDB key could not be decoded. |
| `v8_version` | The V8 serialization wire version is not supported (13 to 16 are). |
| `v8_malformed` | The V8 payload is damaged or fails any other decoding rule. |
| `v8_unknown_tag` | The V8 payload uses a tag teamscrawl does not know. |
| `v8_host_object` | The V8 payload holds a host object, which structured clone does not produce for Teams data. |
| `v8_shared` | The V8 payload holds a SharedArrayBuffer or shared object. |
| `truncated_log_tail` | The active LevelDB log ended mid-record (Teams was writing during the copy). |
| `unmapped_record` | The record decoded, but lacks its identity (for example no message id or conversation id) or is not an object. |

A new omission code may be added in a minor release. The `sources[].omissions` map in a report breaks the totals down by source.

**Report statuses.**

| Status | Meaning | Exit |
| --- | --- | --- |
| `ok` | Every source synced and nothing was omitted. | 0 |
| `ok_with_omissions` | Every source synced; at least one record was omitted. | 0 |
| `unchanged` | No source's cache changed since its last successful sync. | 0 |
| `partial` | At least one source committed and at least one failed (or the run was interrupted after one committed). The report is printed on stdout and a `partial_sync` error on stderr. | 1 |
| `failed` | No source committed. No report is printed, only the coded error of the first failure; the attempt is recorded in `sync_runs` as `failed`. | the error's |

Each entry of `sources` (in every report, not only a partial one) has a `status` (`ok`, `ok_with_omissions`, `unchanged` or `failed`). A source that was decoded and committed also lists its `accounts` (`<tenantId>/<userId>`) and `counts` (`conversations`, `messages`, `people`, `activity`, each with `seen`, `inserted`, `updated`, `unchanged`); an `unchanged` source has neither; a failed source has `error: {code, message}`. The top-level counts add up the committed sources only.

## 5. Commands

Every command accepts the global flags in section 6.1. This section states what each command does and the shape of its result; every flag with its help text is in [docs/commands.md](docs/commands.md).

Commands that run an implicit sync before answering (`--max-age`): `whoami`, `status`, `search`, `messages`, `unread`, `activity`, `thread`, `conversations`, `people`, `sql`. `doctor`, `sync`, `watch`, `skill` and `version` do not.

| Command | Purpose and result |
| --- | --- |
| `doctor` | One check per prerequisite. Result `{"ok": bool, "checks": [{"name", "ok", "warn"?, "detail", "fix"}]}`. Checks, in order: `teams_installed`, `full_disk_access`, `teams_origin`, `database_writable`, `schema_version`, `fts`, `archive_newer`, `archive_upgrade` (only when it applies), `last_sync_status` (only when a run is recorded), `last_sync_age`. `archive_newer` fails when a newer teamscrawl wrote the archive (every sync would refuse it). `archive_upgrade` is a warning: the archive is from an older version and the next `sync` upgrades it. `last_sync_status` is a warning after a `partial` or `failed` run. A warning (`warn: true`, `ok: true`) never fails the run. If any check has `ok: false`, the result still prints on stdout and the command then exits 3 with `doctor_failed`. Non-teamscrawl IndexedDB origins appear in the `teams_origin` detail. |
| `whoami` | `{"accounts": [{"tenant_id", "user_id", "self_id", "display_name", "locale", "first_seen_at", "last_synced_at"}], "archive": {...same body as status...}}`. `self_id` is the account's own sender id. The nested `archive` object carries the same `archive_age_seconds`, `needs_sync` and `hint` as the top level. |
| `sync` | Runs one sync (section 4) and prints the report: `{"status", "sources": [{"source", "status", "omissions"?, "accounts"?, "counts"?, "error"?}], "conversations", "messages", "people", "activity", "omissions", "other_origins", "migrated"?, "started_at", "finished_at"}`. Each entity has `{"seen", "inserted", "updated", "unchanged"}`. `migrated` is `{"from", "to", "rows"}` and appears only on the run that upgraded an older archive. `--account` limits the run to one account. A `partial` run exits 1 (section 4). |
| `status` | `{"archive_path", "archive_exists", "schema_version", "fts_present", "accounts": [{"tenant_id", "user_id", "conversations", "messages", "people", "activity", "newest_sent_at", "last_synced_at"}], "newest_sent_at", "last_run"?, "last_success_at", "other_origins"?}`. |
| `search [query]` | FTS5 over message text, newest first. The query is optional when a filter is given (`--conversation`, `--team`, `--from`, `--since`, `--until`, `--mentions-me`): the filters alone then select the messages. No query and no filter is a `usage` error whose `fix` names `messages`, except on an archive that has never synced with `--max-age 0`, where the sync check runs first and the result is the usual empty list with `needs_sync` (exit 0). Also `--include-deleted`, `--include-system`, `--html`, `--limit`. Items are message items. |
| `messages` | Chronological listing, oldest first (with `--limit`, the newest matches, still oldest first). Same filters as `search`, plus `--unread` and `--include-channels` (which only affects `--unread`). `--conversation` takes an id, an exact title or a display name such as `Team › Channel`. |
| `unread` | Unread messages, newest first. Covers chats and meetings by default; `--include-channels` adds channels and teams. `--by-conversation` returns one item per conversation (`conversation_id`, `conversation_display_name`, `kind`, `unread_count`, `oldest_unread_at`, `newest_unread_at`, `link`), most unread first. Filters: `--conversation`, `--team`, `--include-system`, `--html`, `--limit`. Results carry `"channels_excluded": true` when channels were left out. |
| `activity` | Activity-feed items joined with the message text, sender and conversation display name. Filters: `--unread`, `--type` (comma separated, exact, any case; `mention` and `mentionInChat` are different types), `--team`, `--since`, `--include-system`, `--limit`. |
| `thread <conversation> <root-id>` | One thread, root first. Accepts a Teams message link instead of the two arguments (`.../l/message/<conversationId>/<messageId>`; a `parentMessageId` query parameter names the root). Also `--limit`, `--include-deleted`, `--include-system`, `--html`. |
| `conversations` | Conversations, newest activity first. `--kind`, `--team`, `--query` (ranked: exact name, then prefix, then substring, then all words; each group newest first), `--include-system`. Items add `member_count` and `read_horizon_at`. |
| `people` | People seen as senders or members. `--query` is part of a display name or an exact id; use it to resolve `--from`. |
| `sql <query>` | One read-only `SELECT`, `WITH`, `EXPLAIN` or `VALUES` statement on a read-only connection. Result `{"columns", "rows", "count", "truncated"}`. It streams rows and stops reading at `--limit` (default 50), so `truncated` says more rows exist but there is no `total`. The statement is read as SQL tokens, so a word such as `attach` inside a string, comment or quoted name is harmless; a statement that is not a read, or a second statement, is a `usage` error, as is a query SQLite rejects (a typo, a missing table). Writes are impossible: the connection is read-only. It returns every account's rows regardless of `--account`. |
| `watch` | Runs until interrupted and streams JSON Lines (section 7). |
| `skill` | Prints the agent guide (the repository's `.agents/skills/teamscrawl/SKILL.md`, embedded in the binary) as raw Markdown on stdout, in every output mode, exit 0. It is a documented exception to the JSON default, like `--help`. It needs no archive and no Teams cache. |
| `version` | Prints `{"version", "commit", "date"}` as one JSON document, or one human line (`teamscrawl <version> (commit <commit>, built <date>)`) in text mode. `teamscrawl --version` prints the same and exits 0. |

Filter values: `--since` and `--until` accept RFC3339, `YYYY-MM-DD` (local midnight) or a relative duration such as `90m`, `24h`, `7d`, `2w`. `--from` takes a person id or a case-insensitive part of a name. `--team` takes a team's exact name (case-insensitive for ASCII letters only) or its id; an unknown or ambiguous name is a `usage` error that lists the matches.

## 6. Output contract

### 6.1 Global flags and environment

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--format text\|json\|log` | | Output format. Default: `text` on a terminal, `json` otherwise. `log` prints `<command>=<json>` on one line. |
| `--json` | | Alias for `--format json`. |
| `--db PATH` | `TEAMSCRAWL_DB` | Archive path. |
| `--teams-root DIR` | `TEAMSCRAWL_TEAMS_ROOT` | The EBWebView directory to read. |
| `--account TENANT/USER` | | Limit results (and `sync`) to one account. Default: every account. `whoami` lists the ids. |
| `--no-color` | `NO_COLOR` | Disable ANSI color. `CLICOLOR_FORCE=1` forces it when output is piped. |
| `--max-age DURATION` | `TEAMSCRAWL_MAX_AGE` | Default `15m`. A read command first syncs when the last successful sync is older, or when the archive has never synced. `0` disables the implicit sync. |
| `--version` | | Print the version document and exit 0. |
| `--fields a,b,c` | | List commands only. |
| `--max-text N` | | List commands only. |

`TEAMSCRAWL_DEBUG=1` adds a Go stack trace to stderr after an `internal` error caused by a panic. `--fields` and `--max-text` on a command that is not a list command are a `usage` error.

### 6.2 Streams and documents

- Results go to stdout. Progress, warnings and hints go to stderr. Sync progress lines print only when stderr is a terminal.
- In JSON mode (including every non-TTY run) a command prints exactly one JSON document on stdout, compact on a pipe and indented on a terminal. Two exceptions: `watch` prints JSON Lines (section 7), and `skill` prints raw Markdown in every mode, as `--help` prints help text.
- Keys are snake_case and stable. Adding a field is compatible; renaming or removing one is a breaking change recorded in `CHANGELOG.md`. Optional fields are omitted when empty, except the always-present fields listed below. Times are RFC3339 UTC. Links keep a literal `&`.

### 6.3 List results

Every list command returns:

```json
{"items": [], "count": 0, "truncated": false, "archive_age_seconds": 0}
```

- `items` is always an array. `count` is its length. `--limit` (default 50, at least 1) caps it.
- `truncated` is true when more matches exist than were returned.
- `total` is the exact number of matches ignoring `--limit`. It is present only when `truncated` is true; otherwise `count` is the total. `sql` results never carry `total`.
- `channels_excluded: true` appears on `unread` and `messages --unread` when channels were left out; it is omitted when they are included.
- `archive_age_seconds` (below) and the optional `sync_error`, `needs_sync` and `hint` fields close the document.

### 6.4 Freshness and degraded answers

- `archive_age_seconds` is on every read result (the list commands, `sql`, `status`, `whoami`). It is the number of seconds since the covered accounts were last fully synced, or `null` when no complete sync covers them. "Covered accounts" means the one in `--account`, or, for a read of every account, the stalest account. Only a run in which every source succeeded (`ok`, `ok_with_omissions`, `unchanged`) refreshes an account; a partial or failed run refreshes nobody, and `sync --account X` refreshes only X.
- `needs_sync: true` and `"hint": "run teamscrawl sync"` appear together when there is no complete sync yet: the archive does not exist, has never synced, has only had partial or failed syncs, or was written by an older teamscrawl whose next sync upgrades it. An empty result then reads as "no data yet", not "nothing matched". They are omitted otherwise. In text mode the same hint also prints on stderr as a `hint:` line; in JSON mode there is no stderr hint, because the result carries it.
- If the implicit sync fails, the command still answers from the archive, prints the failure on stderr as `{"warning": {"code", "message", "fix"}}` (a `warning:` and a `fix:` line in text mode) and adds `"sync_error": {"code", "message"}` to the result. A partial implicit sync reports `partial_sync` the same way. Only cancellation by a signal turns it into an error.
- The implicit sync always covers every account, even with `--account`, so a filtered read never hides data from later reads. A held lock makes the implicit sync fail with `locked`, which becomes a `sync_error`.
- Reads work on an archive written by alpha.1 before its first upgrading sync; they treat it as having no complete sync yet.
- `--team` is validated before an implicit sync runs, so a mistyped team name fails fast.

### 6.5 Item shapes

Message items (`messages`, `search`, `unread`, `thread`; also `watch` message lines):

| Field | Rule |
| --- | --- |
| `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `sender_id`, `sender_name`, `sent_at`, `message_type`, `text` | Always present. `sender_name` and `text` may be empty strings. |
| `mentions_me`, `pinned` | Always present, `false` included. |
| `reply_chain_id`, `parent_message_id`, `importance`, `link` | Present when known. `link` is a Teams deep link `https://teams.microsoft.com/l/message/<conversationId>/<messageId>?tenantId=...`; a reply's link adds `parentMessageId`. |
| `edited_at`, `deleted_at`, `mentions`, `reactions`, `files`, `links`, `subject`, `html`, `text_truncated` | Omitted when empty or false. `html` appears only with `--html`. |
| `reply_count`, `last_reply_at` | Channel thread roots only. `reply_count` is the number of live replies stored for the root, computed at query time from archived replies, so it can be lower than Teams shows if the cache never held the replies. A root nobody answered has `reply_count: 0` and no `last_reply_at`. A message with no `reply_count` key (a reply, any chat message) is not a channel root. |

`mentions` items are `{id, display_name}`; `reactions` items `{key, count, user_ids?}`; `files` items `{name, url?, type?}`.

Conversation items: `tenant_id`, `user_id`, `id`, `kind`, `display_name`, `member_count` and `favorite` are always present; `title`, `team_id`, `last_message_at` and `read_horizon_at` are omitted when empty.

Person items: `tenant_id`, `id`, `display_name`, and `last_seen_at` when known.

Activity items: `tenant_id`, `user_id`, `id`, `type`, `is_read`, `at`, `conversation_display_name`, `sender_name` and `text` are always present; `subtype`, `conversation_id`, `message_id`, `reply_chain_id`, `app_id`, `sender_id`, `message_sent_at`, `text_truncated` and `link` are omitted when empty. Activity types seen in real caches: `mention`, `mentionInChat`, `reply`, `replyToReply`, `follow`, `reaction`, `reactionInChat`, `msGraph`, `teamMembershipChange`, `threadActivity`. An unknown `--type` matches nothing and is not an error.

### 6.6 `--fields`, `--max-text` and `text_truncated`

- `--fields a,b,c` keeps only those top-level keys of each item, in the order given. An unknown key is a `usage` error that lists the valid keys for that command. On `watch`, a key valid for either a message or an activity item is accepted. The list envelope keys (`count`, `truncated`, `total`, `archive_age_seconds`, ...) are never projected away.
- `--max-text N` cuts each item's `text` to N characters, counting the trailing `…` the cut adds (the cut may fall mid-word), and sets `"text_truncated": true` on the cut items. `0` keeps all text. A negative value is a `usage` error: `--max-text=-1` prints `--max-text must be 0 or more`, and `--max-text -1` (separate argument) is rejected by the argument parser with `--max-text: expected int value but got "-1" ...`. When `--fields` includes `text`, `text_truncated` stays with it.

### 6.7 Errors

Errors go to stderr, never stdout, as one line `{"error": {"code", "message", "fix"}}` in JSON and log mode, or `error: <message>` and `fix: <fix>` lines in text mode. `fix` is an instruction the caller can follow. A Go stack trace is never printed unless `TEAMSCRAWL_DEBUG=1` is set. The process exit status follows the error's code (section 6.8).

Every error code:

| Code | Exit | When | Typical `fix` |
| --- | --- | --- | --- |
| `usage` | 2 | A bad flag, argument, filter, `--fields` key, `--account`, `--team`, time value, link or SQL statement; also a flag given to a command that does not accept it. | `Run `teamscrawl <command> --help` to see the accepted arguments and flags.` Many usage errors carry a more specific fix. |
| `snapshot_inconsistent` | 1 | Teams kept changing the cache during all three copy attempts, or the copy names a missing file or a truncated manifest. | `Run the command again; if Teams is busy syncing, quit Teams or wait a minute first.` |
| `unsupported_block_compression` | 1 | A LevelDB table uses a block compression other than none or snappy. Nothing is dropped silently. | `Update teamscrawl; if it is already current, report the issue with the output of `teamscrawl doctor`.` |
| `store_missing` | 1 | An allowlisted Teams database exists but has no expected object store, so Teams changed its storage layout or has not finished loading. | `Open Teams, let it finish loading, and run again; if it persists Teams changed its storage layout, so update teamscrawl.` |
| `db_error` | 1 | The archive cannot be created, opened, read or written, or a LevelDB or IndexedDB structure is unreadable for a reason not listed above. The message ends with the underlying cause. | `Check that the archive path is writable and has free space; run `teamscrawl doctor`.` |
| `partial_sync` | 1 | A sync committed at least one source and failed at least one. The message names each failed source and its code. The sync report is on stdout, so a caller sees both. | `Run `teamscrawl doctor` to see what is wrong with the failing source, fix it and run `teamscrawl sync` again; the sources that synced are already in the archive.` |
| `interrupted` | 1 | SIGINT or SIGTERM stopped the command before it finished. Each source is one transaction, so nothing is half-written. (`watch` exits 0 on a signal.) A second SIGINT or SIGTERM during the stop quits at once with exit 130 and no error document; it can leave a snapshot directory in the temp directory, which a later sync removes once it is older than an hour. | `Run the command again.` |
| `internal` | 1 | A teamscrawl bug, an unexpected failure, or a recovered panic. | `This is a bug in teamscrawl; report it with the command you ran.` (a panic: re-run with `TEAMSCRAWL_DEBUG=1` and report the output at the issues page.) |
| `teams_not_installed` | 3 | The EBWebView directory does not exist. | `Install the new Microsoft Teams app and sign in once.` |
| `no_full_disk_access` | 3 | macOS denied access to the Teams container (`EPERM`). | `Open System Settings > Privacy & Security > Full Disk Access, turn it on for <the app that runs teamscrawl>, then quit and reopen that app and run the command again.` The app name is derived from the process tree. |
| `no_teams_origin` | 3 | No profile holds a Teams origin, so Teams has not created its cache. | `Open Teams and sign in so it creates its cache, then run again.` |
| `doctor_failed` | 3 | At least one required `doctor` check failed. The `doctor` result is on stdout; the message lists the failing checks. | `Resolve the failing checks listed by `teamscrawl doctor`, then run again.` |
| `archive_newer` | 3 | The archive was written by a newer teamscrawl (higher derivation version). `sync`, `watch` and the implicit sync refuse before any write; read commands still work. | `Upgrade teamscrawl (brew upgrade ourostack/tap/teamscrawl), or point --db at a different archive.` |
| `locked` | 4 | Another sync or `watch` holds `<db>.lock`. | `Wait for the other teamscrawl run to finish, then run again.` |

Warning codes (never an exit status; emitted as `{"warning": ...}` on stderr, or as a `watch` error line): any error code above when an implicit sync fails, plus `baseline_delayed` and, from `watch` when file events cannot start, an `internal` warning that says it is polling (section 7).

### 6.8 Exit codes

| Exit | Meaning | Codes |
| --- | --- | --- |
| 0 | Success, including a sync with status `ok`, `ok_with_omissions` or `unchanged`, and `watch` stopped by a signal. | none |
| 1 | Runtime failure. | `snapshot_inconsistent`, `unsupported_block_compression`, `store_missing`, `db_error`, `partial_sync`, `interrupted`, `internal` |
| 2 | Usage error. | `usage` |
| 3 | Environment not ready. | `teams_not_installed`, `no_full_disk_access`, `no_teams_origin`, `doctor_failed`, `archive_newer` |
| 4 | Another run holds the lock. | `locked` |
| 130 | A second SIGINT or SIGTERM forced an immediate quit. | none |

## 7. `watch`

`watch [--every 60s] [--emit-initial]` keeps the archive current and tells the caller what changed. It is the one command that prints more than one JSON document: it prints **JSON Lines**, one compact object per line on stdout, flushed as each event happens. Every line has a `kind`.

How it runs:

1. It syncs once at start. By default that first sync is a silent baseline: its changes are not printed, so only later changes appear. `--emit-initial` prints them too.
2. It watches the Teams `.leveldb` and `.blob` directories with file-system events (kqueue on macOS). Events are debounced: a sync starts after the cache has been quiet for 2 seconds, at least 5 seconds after the previous sync started, and at most 10 seconds after the first event of a burst. A poll every `--every` (default 60 seconds) is the safety net when events are missed; it syncs only when the source fingerprint changed. If file events are unavailable, `watch` warns on stderr and polls.
3. It runs until SIGINT or SIGTERM, then exits 0.

Line kinds:

| `kind` | Shape | When |
| --- | --- | --- |
| `message` | `{"kind": "message", "change": "new" \| "edited" \| "deleted", "item": <message item>}` | A message was inserted, changed or newly tombstoned by a sync. |
| `activity` | `{"kind": "activity", "change": "new" \| "edited", "item": <activity item>}` | An activity item was inserted or changed. |
| `sync` | `{"kind": "sync", "report": <sync report>}` | After each sync that ran and emitted, one line, after that sync's change lines. |
| `migrated` | `{"kind": "migrated", "from": 1, "to": 2, "rows": N}` | The first sync upgraded an older archive. Printed before anything else, even for a silent baseline. It is not a change: no `edited` lines follow for those rows. |
| `error` | `{"kind": "error", "error": {"code", "message", "fix"}}` | A sync or check failed with a runtime error, including `partial_sync`. Watching continues, and an unchanged repeat of the same failure is reported once. |

`item` has the same shape as the matching `messages` or `activity` item and honors `--fields`, `--max-text` and `--account`. System pseudo-conversations are skipped. `change: "deleted"` means the message gained its `deleted_at`.

Partial syncs: when a sync commits some sources and fails others (`partial_sync`), `watch` first emits the changes of the committed sources and a `sync` line with `"status": "partial"`, then the `error` line. The fingerprints stay as they were, so the next pass retries only what failed and no change is lost. The first sync that commits anything is the baseline, exactly as for a normal first sync: its changes are printed only with `--emit-initial`. `--fields html` fills `html` as `messages --html` does.

Failures: a held lock prints a `locked` warning on stderr and retries after at most 5 seconds. Environment errors (`teams_not_installed`, `no_full_disk_access`, `no_teams_origin`, `archive_newer`) end the run with a plain error on stderr and exit 3. If the first sync fails and a later one succeeds, that later sync is taken as the baseline, its changes are not printed, and a `baseline_delayed` warning on stderr says so (its `fix` suggests reading the archive, or restarting with `--emit-initial`). In text mode (`--format text`) `watch` prints one human line per event instead of JSON.

Latency: a change reaches the output after Teams flushes it to its cache, plus the debounce (2 to 10 seconds) and one sync. Measured on a real cache, a message's sent time to its line took about 12 to 32 seconds (median about 17 seconds), most of it Teams' own flush delay.

## 8. Privacy and security

- **Read-only by construction.** There is no code path that writes to Teams' storage. The Teams cache is only ever opened for reading, and `sql` runs on a read-only database connection (writes, `ATTACH` and multi-statement queries are rejected before and by the connection).
- **No credentials, no network.** teamscrawl never reads Teams tokens. The `Teams:auth:*` database is never decoded. The program has no network code.
- **The archive holds message content verbatim.** That includes anything people pasted into a message: links with tokens or secrets, credentials, file names, customer data. `raw_json` keeps the full original record. Treat the archive like the chats it contains: do not commit it, sync it to shared storage or paste its contents into tools you would not show the original messages to.
- **File permissions.** The archive file and its `<db>.lock` file are mode 0600. The default archive directory `~/.teamscrawl` is mode 0700; a custom `--db` parent directory is created 0700 if missing and otherwise left as it is.
- **Snapshots.** The temporary copy of the cache includes Teams' sign-in database. It lives in a 0700 directory in the temp directory for the length of one source's sync and is removed on success, on failure, and on SIGINT or SIGTERM. Stale snapshot directories older than one hour are removed at the start of every sync.
- **Full Disk Access** is the only permission teamscrawl needs (see [docs/full-disk-access.md](docs/full-disk-access.md)).
- **Tests and docs use synthetic data only.** The committed fixture is written by a real browser from made-up records.

## 9. Known limits

- teamscrawl sees only what the desktop app has cached. History the user never scrolled to may be missing, and Teams evicts old messages from its cache, so the archive is complete only from the first sync that saw a message onward. Sync regularly (`--max-age` does it for you).
- macOS and the new Teams app only. Classic Teams, Windows and Linux are not supported. Contact stores, calendar, call history and pinned-message lists are not mirrored.
- Full Disk Access is required for the app that runs teamscrawl.
- Read-only: no sending, reacting or marking read. Attachments and media are not downloaded.
- `reply_count` and `last_reply_at` are counted from archived replies and can lag Teams.
- People display names follow the most recent sighting; after an archive upgrade a few can differ from what a fresh sync would give.
- Teams can change its storage layout at any time. teamscrawl then fails with a named error or reports counted omissions; it never guesses. A new V8 wire version fails with the version.
- A multi-source sync is atomic per source, not per run: a `partial` sync leaves the committed sources in the archive (section 4).
- `watch` latency is dominated by when Teams writes its cache, not by teamscrawl.
- Release binaries are Developer ID signed and notarized only when the release was built with the Apple signing secrets; the release notes of each release say which.

## 10. Versioning and compatibility

- The JSON contract in section 6 is stable within 0.x releases except where `CHANGELOG.md` records a break. Fields may be added; error codes, omission codes and activity types may be added.
- The archive has two version numbers: the SQLite schema version (2) and the derivation version (2, section 3.2). An older archive is upgraded in place by the first `sync`. An archive written by a newer build is refused for writing with `archive_newer`.
- The source fingerprint includes a decoder version; raising it makes the next sync re-read an unchanged cache.
