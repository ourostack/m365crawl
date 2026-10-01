---
name: teamscrawl
description: Use when an agent needs to read the user's Microsoft Teams messages, chats, channels, mentions, unread state or activity feed on this Mac, through the local `teamscrawl` CLI, which mirrors the Teams desktop cache into a searchable SQLite archive.
---

# teamscrawl

Read-only, offline access to the user's Teams history. It copies the new Teams desktop app's local cache into SQLite (`~/.teamscrawl/teamscrawl.db`) and answers from there. It cannot send, react or mark read.

## When to use

- Use it to answer "what is unread", "what mentions me", "what happened in channel X", "what was said about Y", or to summarize a thread.
- Do not use it to send or react (no write path exists), to fetch files or media (metadata only), or for anything outside macOS new Teams. It knows only what the desktop app cached, so very old history may be missing.

## 60-second workflow

```sh
teamscrawl doctor                      # once; exit 3 means fix the environment first
teamscrawl whoami                      # which accounts, how fresh
teamscrawl unread --limit 20 --max-text 300
teamscrawl activity --unread --limit 20 --max-text 300
teamscrawl search "quarterly plan" --since 7d --limit 10 --max-text 300
teamscrawl thread <conversation_id> <root_message_id>   # or: teamscrawl thread "<link>"
```

No text query? Use `messages` with filters; `search` needs at least one word.

Output is JSON when stdout is not a terminal (pass `--json` to be sure). Read commands sync first when the archive is older than `--max-age`; pass `--max-age 15m` for fresh answers or `--max-age 0` to skip the sync.

## Global flags

`--json`, `--db PATH` (`TEAMSCRAWL_DB`), `--teams-root DIR` (`TEAMSCRAWL_TEAMS_ROOT`), `--account <tenantId>/<userId>` (default every account), `--max-age DURATION` (`TEAMSCRAWL_MAX_AGE`), `--fields a,b,c`, `--max-text N`.

List results are `{"items":[...],"count":N,"truncated":bool,"archive_age_seconds":N}`. `truncated: true` means more exist: raise `--limit` (default 50) or narrow the filters. Times are RFC3339 UTC. Sort orders: `search` and `unread` are newest first, `messages` is chronological (oldest first; with `--limit` you get the newest matches), `conversations` is sorted by last activity, newest first. Every list stops at `--limit` (default 50), so check `truncated`. Filter time flags accept RFC3339, `YYYY-MM-DD` (local midnight) or relative `90m`, `24h`, `7d`, `2w`.

## Commands

If the archive has never had a successful sync, every read result also carries `"needs_sync":true` and `"hint":"run teamscrawl sync"` (both omitted otherwise). An empty result with `needs_sync` means no data yet, not no match: run `teamscrawl sync` and repeat.

System pseudo-conversations (`48:notifications`, `48:calllogs`, `48:annotations`) are excluded by default from `search`, `messages`, `unread`, `thread`, the message text in `activity`, and `conversations`, because their messages duplicate real ones with blank sender and conversation names. Pass `--include-system` to bring them back. Your own notes (`48:notes`) stay included.

An @-mention appears in `text` as the person's plain name; `mentions` lists who was mentioned and `mentions_me` is exact.

### whoami

Accounts in the archive and its state. Use `self_id` to recognize the user's own messages.

```json
{"accounts":[{"tenant_id":"00000000-0000-4000-8000-000000000001","user_id":"00000000-0000-4000-8000-0000000000a1","self_id":"8:orgid:00000000-0000-4000-8000-0000000000a1","display_name":"Alex Fixture","locale":"en-us"}],"archive":{"schema_version":1,"fts_present":true},"archive_age_seconds":0}
```

### unread

Unread messages, newest first (newer than the conversation's read marker and not sent by the user). By default it covers chats and meetings only: most channels are never opened, so their unread counts are noise, and channel mentions and replies reach you through `activity`. Add `--include-channels` to count channels and teams too (`messages --unread` takes the same flag). `--by-conversation` gives the overview instead of messages: one item per conversation `{conversation_id, conversation_display_name, kind, unread_count, oldest_unread_at, newest_unread_at, link}`, most unread first; start there, then read a conversation with `messages -c <id> --unread` (pass `--include-channels` too when that conversation is a channel, or the channel's messages are filtered out). When channels are left out (no `--include-channels`), `unread` and `messages --unread` results carry `"channels_excluded":true`; it is omitted when channels are included. Flags: `-c/--conversation`, `--limit`, `--html`, `--include-channels`, `--by-conversation`, `--include-system`.

```json
{"items":[{"conversation_id":"19:topicchannel1@thread.tacv2","conversation_display_name":"Fixture team 1 › General","id":"1700000046000","reply_chain_id":"1700000045000","sender_name":"Pat Example","sent_at":"2023-11-14T22:14:06Z","text":"Channel post with a subject\nReply in thread","mentions_me":false,"link":"https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?tenantId=...&parentMessageId=1700000045000"}],"count":1,"truncated":false,"archive_age_seconds":0}
```

### activity

The Teams activity feed (mentions, replies, reactions, follows) joined with message text, sender and conversation. Flags: `--unread`, `--type mentionInChat`, `--since 1h`, `--limit`, `--include-system`.

```json
{"items":[{"id":"fixture-activity-1-8","type":"follow","is_read":false,"at":"2023-11-14T22:22:00Z","conversation_display_name":"Fixture team 1 › Planning","message_id":"1700000047000","sender_name":"Pat Example","text":"Planning channel message","link":"https://teams.microsoft.com/l/message/19:planningchannel1@thread.tacv2/1700000047000?tenantId=..."}],"count":1,"truncated":false,"archive_age_seconds":0}
```

### search

Full-text (FTS5) over message text, newest first. Supports `"quoted phrases"` and a trailing `*` prefix. Flags: `-c/--conversation` (id, exact title or display name such as `"Team › Channel"`), `--from` (person id or part of a name), `--since`, `--until`, `--mentions-me`, `--include-deleted`, `--html`, `--include-system`, `--limit`. Items have the same shape as `messages`.

### messages

Chronological listing (oldest first) with the same filters as `search`, plus `--unread` and `--include-channels` (which only affects `--unread`). Use `-c "Team › Channel" --since 1d` to read a channel.

```json
{"items":[{"conversation_display_name":"Fixture chat 1","id":"1700000039000","sender_name":"Pat Example","sent_at":"2023-11-14T22:13:59Z","text":"Alex Fixture and Sam Tag see this","mentions":[{"id":"8:orgid:00000000-0000-4000-8000-0000000000a1","display_name":"Alex Fixture"}],"mentions_me":true,"importance":"normal","pinned":false,"link":"https://teams.microsoft.com/l/message/..."}],"count":1,"truncated":false,"archive_age_seconds":0}
```

Optional keys (omitted when empty): `edited_at`, `deleted_at`, `mentions`, `reactions`, `files`, `links`, `subject`, `html`.

### thread

One thread as `items`, root first. Pass `<conversation_id> <root_message_id>` (the root is an item's `parent_message_id`/`reply_chain_id`) or a Teams message link copied from any `link`. Flags: `--limit` (default 50; check `truncated`), `--include-deleted`, `--html`, `--include-system`.

### conversations and people

`conversations --kind Chat|Topic|Space|Meeting --query words [--include-system]` lists conversations, sorted by last activity, newest first (default `--limit 50`, check `truncated`), with `display_name`, `kind`, `last_message_at`, `read_horizon_at`, `favorite`. `people --query name` resolves a name to a `sender_id` for `--from`.

### sql

One read-only SELECT (or WITH/EXPLAIN/VALUES), for counts and joins the commands do not cover. Returns `{"columns":[...],"rows":[[...]],"count":N,"truncated":bool}`. Tables: `accounts`, `conversations`, `messages`, `people`, `activity`, `sync_runs`; FTS tables `message_fts`, `conversation_fts`. Note that `sql` returns every account's rows regardless of `--account`.

### watch

`watch [--every 60s] [--emit-initial]` runs until interrupted (SIGINT or SIGTERM exit 0) and prints JSON Lines, the one exception to the one-document rule. It syncs once at start as a silent baseline (nothing is printed unless `--emit-initial`), then syncs whenever the Teams cache changes (file events, with a poll every `--every` as the safety net; bursts are debounced) and prints one line per change followed by one report line:

```
{"kind":"message","change":"new","item":{"conversation_display_name":"...","sender_name":"...","text":"...","link":"..."}}
{"kind":"activity","change":"edited","item":{"type":"mentionInChat","is_read":true,"...":"..."}}
{"kind":"sync","report":{"status":"ok","messages":{"seen":104,"inserted":1,"updated":0,"unchanged":103},"...":"..."}}
```

`change` is `new`, `edited` or `deleted`; `item` has the same shape as a `messages` or `activity` item and honors `--fields`, `--max-text` and `--account` (system pseudo-conversations are skipped). A failed sync prints `{"kind":"error","error":{"code","message","fix"}}` and watching continues; a locked archive prints a `locked` warning on stderr and retries. Environment errors (`teams_not_installed`, `no_full_disk_access`, `no_teams_origin`) end the run with exit 3. Run it in the background and read its stdout; use `--fields` and `--max-text` to keep lines small.

### doctor, sync, status

- `doctor` returns `{"ok":bool,"checks":[{"name","ok","warn"?,"detail","fix"}]}`. A warning (for example `last_sync_age` "never synced") does not fail it. Follow each failing check's `fix`.
- `sync` returns counts (`seen`, `inserted`, `updated`, `unchanged`) per entity, `omissions` by reason, and `status` of `ok`, `ok_with_omissions` or `unchanged`. All exit 0.
- `status` shows per-account counts, the last run and other Teams origins seen.

## Freshness

- Every read result has `archive_age_seconds`. If it matters that the answer is current, rerun with `--max-age 5m` or run `teamscrawl sync`.
- If the implicit sync fails, the command still answers from the archive, prints a warning on stderr (`{"warning":{"code","message","fix"}}` in JSON mode) and adds a `sync_error` field to the result. Treat the answer as possibly stale and follow the warning's `fix` if freshness matters.
- Teams evicts old messages from its own cache. The archive keeps them, but only what a sync has seen, so sync regularly (for example `--max-age 1h` in every call).

## Context budget

Messages can be long. Start with `--max-text 300` (sets `text_truncated: true` on cut items; it keeps the first N characters including the trailing `…` and may cut mid-word), `--fields id,sender_name,sent_at,text,link` to drop the rest, and a small `--limit`. Fetch full text later for the few items that matter with `thread` or `messages -c ... --limit 1`. Fields accepted by `--fields` are the item's top-level keys; an unknown key is a `usage` error that lists the valid ones.

## Citing

Every item has a `link`, a Teams deep link (`https://teams.microsoft.com/l/message/<conversationId>/<messageId>?tenantId=...`). Cite it so the user can open the message in Teams. Also give `conversation_display_name`, `sender_name` and `sent_at`.

## Errors

Errors go to stderr as `{"error":{"code","message","fix"}}` and set the exit status. Follow `fix`.

| Exit | Code | Meaning and action |
| --- | --- | --- |
| 2 | `usage` | Bad flag, filter, `--fields` key, account or query. Fix the command; `--help` shows options. |
| 3 | `teams_not_installed` | The new Teams data directory is missing. Ask the user to install new Teams and sign in. |
| 3 | `no_full_disk_access` | macOS blocked the read. The `fix` names the app (your terminal or agent host) to grant in System Settings > Privacy & Security > Full Disk Access; it must be restarted. Ask the user. |
| 3 | `no_teams_origin` | Teams has no cache yet. Ask the user to open Teams and sign in, then retry. |
| 3 | `doctor_failed` | A required `doctor` check failed. Run `teamscrawl doctor` and follow the failing check's `fix`. |
| 4 | `locked` | Another sync is running. Wait and retry. |
| 1 | `snapshot_inconsistent` | Teams was writing while the cache was copied. Retry once; if it persists, ask the user to quit Teams briefly. |
| 1 | `unsupported_block_compression` | The cache uses a format this version cannot read. Update teamscrawl and report it with `doctor` output. |
| 1 | `store_missing` | A Teams store vanished. Ask the user to open Teams until it loads, then retry; if it persists, update teamscrawl. |
| 1 | `db_error` | The archive cannot be read or written. Check `--db` path, permissions and disk space. |
| 1 | `interrupted` | The command was stopped (Ctrl-C or a signal) before it finished; nothing was half-written. Run it again. |
| 1 | `internal` | A teamscrawl bug. Report the command you ran. |

Recovering from an empty or stale archive: run `teamscrawl sync`, then repeat the read. An empty result after a successful sync means the desktop cache has nothing matching, not that teamscrawl failed.

## Rules

- Never paste message content into places the user did not expect; the archive is private.
- Never run anything that edits Teams' folders or the archive by hand; use the commands.
