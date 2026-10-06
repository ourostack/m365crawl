---
name: teamscrawl
description: Use when an agent needs to read the user's Microsoft Teams messages, chats, channels, mentions, unread state, activity feed, meetings or calendar on this Mac or Windows PC, through the local `teamscrawl` CLI, which mirrors the Teams desktop cache into a searchable SQLite archive.
---

# teamscrawl

Read-only, offline access to the user's Teams history, and to everything else the desktop app cached except sign-in credentials. It copies the new Teams desktop app's local cache into SQLite (`~/.teamscrawl/teamscrawl.db` on macOS, `%LOCALAPPDATA%\teamscrawl\teamscrawl.db` on Windows) and answers from there. It cannot send, react or mark read. The full contract is in SPEC.md in the teamscrawl repo (https://github.com/ourostack/teamscrawl/blob/main/SPEC.md); this file is the short version. Run `teamscrawl skill` to print this guide from the installed binary (raw Markdown in every mode), so it always matches the version you are running.

## When to use

- Use it to answer "what is unread", "what mentions me", "what happened in channel X", "what was said about Y", "what meetings do I have", "what was decided in meeting Z" or "what action items are mine", or to summarize a thread.
- Do not use it to send or react (no write path exists), to fetch files or media (metadata only), or for anything outside macOS or Windows new Teams. It knows only what the desktop app cached, so very old history may be missing.

## 60-second workflow

```sh
teamscrawl doctor                      # once; exit 3 means fix the environment first
teamscrawl whoami                      # accounts (self_id is the user's own sender id), how fresh
teamscrawl unread --by-conversation --since 7d   # "what needs my attention": recent unread only
teamscrawl unread --limit 20 --max-text 300
teamscrawl activity --unread --limit 20 --max-text 300
teamscrawl search "quarterly plan" --since 7d --limit 10 --max-text 300
teamscrawl thread <conversation_id> <root_message_id>   # or: teamscrawl thread "<link>"
```

No words to search for? `search` also works with filters alone (`search --mentions-me --since 24h`, newest first); with neither words nor a filter it is a usage error whose fix names `messages`.

Output is JSON when stdout is not a terminal (pass `--json` to be sure). Read commands sync first when the archive is older than `--max-age` (stderr gets one line first: plain text in text mode, `{"notice":"syncing","reason":"stale","archive_age_seconds":N,"max_age_seconds":N}` in JSON mode; the result gains `synced: {seconds, status}`); pass `--max-age 15m` for fresh answers or `--max-age 0` to skip the sync.

## Supported hosts and install

- macOS: install with Homebrew (`brew install ourostack/tap/teamscrawl`) or the darwin release tarballs. Full Disk Access is required for the app that launches `teamscrawl`.
- Windows: download the release zip (`teamscrawl_<version>_windows_amd64.zip` or `teamscrawl_<version>_windows_arm64.zip`), unzip it, and run `teamscrawl.exe`. Windows release binaries are intentionally unsigned. `teamscrawl doctor` still checks the environment, but `full_disk_access` is `ok: true` / `not applicable on Windows`.
- Linux is not supported because there is no supported Teams desktop cache path to mirror.

## Global flags

`--json` (or `--format text|json|log`), `--no-color`, `--db PATH` (`TEAMSCRAWL_DB`), `--teams-root DIR` (`TEAMSCRAWL_TEAMS_ROOT`), `--account <tenantId>/<userId>` (default every account), `--max-age DURATION` (`TEAMSCRAWL_MAX_AGE`), `--fields a,b,c`, `--max-text N`. Platform defaults: macOS `--teams-root ~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView`, Windows `--teams-root %LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView`; macOS `--db ~/.teamscrawl/teamscrawl.db`, Windows `--db %LOCALAPPDATA%\teamscrawl\teamscrawl.db`.

On Windows the default archive path is private by construction. A custom `--db` path is allowed only when its direct parent directory is already private to the current user and SYSTEM or teamscrawl can create that parent itself; otherwise `sync`, `watch`, and any implicit sync fail with `db_error` before SQLite opens the database.

List results are `{"items":[...],"count":N,"truncated":bool,"archive_age_seconds":N}`. `truncated: true` means more exist: raise `--limit` (default 50) or narrow the filters. When `truncated` is true the result also has `"total":N`, the exact number of matches ignoring `--limit` (so you can tell 51 from 5,000 before deciding to page); it is omitted when not truncated, because then `count` is the total. `sql` stops reading at `--limit`, so it sets `truncated` but never `total`. Times are RFC3339 UTC. Sort orders: `search` and `unread` are newest first, `messages` is chronological (oldest first; with `--limit` you get the newest matches), `conversations` is sorted by last activity, newest first. Every list stops at `--limit` (default 50), so check `truncated`. Filter time flags accept RFC3339, `YYYY-MM-DD` (local midnight) or relative `90m`, `24h`, `7d`, `2w`.

## Commands

If the archive has no complete sync yet, every read result also carries `"needs_sync":true` and `"hint":"run teamscrawl sync"` (both omitted otherwise). `needs_sync` means no complete sync yet: the archive has never synced, or only partial or failed syncs so far, or it was written by an older teamscrawl whose next sync upgrades it. An empty result with `needs_sync` means no data yet, not no match: run `teamscrawl sync` and repeat.

System pseudo-conversations (`48:notifications`, `48:calllogs`, `48:annotations`) are excluded by default from `search`, `messages`, `unread`, `thread`, the message text in `activity`, and `conversations`, because their messages duplicate real ones with blank sender and conversation names. Pass `--include-system` to bring them back. Your own notes (`48:notes`) stay included.

An @-mention appears in `text` as the person's plain name; `mentions` lists who was mentioned and `mentions_me` is exact. When `mentions_me` is true, `mention_kind` says how: `person` (you by name), `channel`, `team`, `tag`, `everyone` (a broadcast) or `other`. `--direct-mentions` (on `messages`, `search`, `activity`; same meaning in all three) keeps only `person` mentions, so use it to leave @channel and @team noise out of "who mentioned me".

`text` is always readable, never raw card JSON or markup: bot cards give their title, text blocks and facts, call and thread events read like `Call ended · 23m` or `Member added`, and `raw_json` (via `sql`) keeps the original. A few messages have empty `text` (deleted, image- or file-only, meeting notices). Details, and how an alpha.1 archive is upgraded by its first `sync`, are in SPEC.md section 3.

`--team <name|id>` (on `messages`, `search`, `unread`, `activity`, `conversations`) limits results to one team, meaning the team's own conversation and all of its channels. Give the team's exact name (case-insensitive for ASCII letters only, so `Équipe` and `équipe` differ), or its id (`teamscrawl teams` lists both). A name that matches no team, or more than one, is a `usage` error; the ambiguous case lists each match as `name (id)`, so retry with the id.

### unread

Unread messages, newest first (newer than the conversation's read marker and not sent by the user). By default it covers chats and meetings only: most channels are never opened, so their unread counts are noise, and channel mentions and replies reach you through `activity`. Add `--include-channels` to count channels and teams too (`messages --unread` takes the same flag). `--by-conversation` gives the overview instead of messages: one item per conversation `{conversation_id, conversation_display_name, kind, unread_count, oldest_unread_at, newest_unread_at, link}`, most unread first; start there, then read a conversation with `messages -c <id> --unread` (pass `--include-channels` too when that conversation is a channel, or the channel's messages are filtered out). When channels are left out (no `--include-channels`), `unread` and `messages --unread` results carry `"channels_excluded":true`; it is omitted when channels are included. Old read markers leave stale conversations with hundreds of unread messages, so for "what needs my attention" pass `--since 7d`: only unread messages sent at or after the cutoff count (also with `--by-conversation`, `--team`, `--include-channels`). Flags: `-c/--conversation`, `--team`, `--since`, `--limit`, `--html`, `--include-channels`, `--by-conversation`, `--include-system`.

```json
{"items":[{"conversation_id":"19:topicchannel1@thread.tacv2","conversation_display_name":"Fixture team 1 › General","id":"1700000046000","reply_chain_id":"1700000045000","sender_name":"Pat Example","sent_at":"2023-11-14T22:14:06Z","text":"Channel post with a subject\nReply in thread","mentions_me":false,"link":"https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?tenantId=...&parentMessageId=1700000045000"}],"count":1,"truncated":false,"archive_age_seconds":0}
```

### activity

The Teams activity feed (mentions, replies, reactions, follows) joined with message text, sender and conversation. Flags: `--unread`, `--type mention,mentionInChat` (comma separated, exact, case-insensitive for ASCII letters; a list naming no type is a `usage` error), `--direct-mentions`, `--team`, `--since 1h`, `--limit`, `--include-system`.

Activity `type` values: `mention` (@-mentioned in a channel) and `mentionInChat` (in a chat) are different types, so for everything that mentions you use `--type mention,mentionInChat` or `search --mentions-me`; also `reply`, `replyToReply`, `follow`, `reaction`, `reactionInChat`, `msGraph` (Microsoft 365 notices), `teamMembershipChange`, `threadActivity`. `--type` takes the type, not the subtype; an unknown type matches nothing and is not an error. `actor_id` and `actor_name` say who did it (who reacted, replied, mentioned or posted) and differ from `sender_*`, which is the related message's author: for `reaction` and `reactionInChat` the actor is the reactor, inferred from the reaction nearest in time and flagged `actor_inferred: true` (best effort: it can be wrong when several people reacted at once), for `mention`, `mentionInChat`, `reply`, `replyToReply` and `follow` it is the message's sender, and `msGraph`, `teamMembershipChange` and `threadActivity` have none (the keys are omitted, as they are when the message is not archived). `msGraph` rows often have empty conversation, sender and text.
```json
{"items":[{"id":"fixture-activity-1-8","type":"follow","is_read":false,"at":"2023-11-14T22:22:00Z","conversation_display_name":"Fixture team 1 › Planning","message_id":"1700000047000","sender_name":"Pat Example","text":"Planning channel message","link":"https://teams.microsoft.com/l/message/19:planningchannel1@thread.tacv2/1700000047000?tenantId=..."}],"count":1,"truncated":false,"archive_age_seconds":0}
```

### search

Full-text (FTS5) over message text, newest first. Supports `"quoted phrases"` and a trailing `*` prefix. The query is optional when a filter is given: `search --mentions-me --since 7d` lists what the filters select, newest first. Flags: `-c/--conversation` (id, exact title or display name such as `"Team › Channel"`), `--team`, `--from` (person id or part of a name), `--since`, `--until`, `--mentions-me`, `--include-deleted`, `--html`, `--include-system`, `--limit`. Items have the same shape as `messages`.

### messages

Chronological listing (oldest first) with the same filters as `search`, plus `--unread` and `--include-channels` (which only affects `--unread`). Use `-c "Team › Channel" --since 1d` to read a channel.

```json
{"items":[{"conversation_display_name":"Fixture chat 1","id":"1700000039000","sender_name":"Pat Example","sent_at":"2023-11-14T22:13:59Z","text":"Alex Fixture and Sam Tag see this","mentions":[{"id":"8:orgid:00000000-0000-4000-8000-0000000000a1","display_name":"Alex Fixture"}],"mentions_me":true,"importance":"normal","pinned":false,"link":"https://teams.microsoft.com/l/message/..."}],"count":1,"truncated":false,"archive_age_seconds":0}
```
Optional keys (omitted when empty): `edited_at`, `deleted_at`, `mentions`, `reactions`, `files`, `links`, `subject`, `html`, `reply_count`, `last_reply_at`.

`reply_count` and `last_reply_at` appear on channel thread roots only (the number of live replies stored for that root, and when the newest was sent), in `messages`, `search`, `unread` and `thread`. A root nobody answered has `reply_count: 0` and no `last_reply_at`; a message with no `reply_count` key (a reply, any chat message) is not a channel root. They are counted at query time from the archived replies, so they can lag what Teams shows if the cache never held the replies. Read the thread with `thread <conversation_id> <id>`.

### thread

One thread as `items`, root first. Pass `<conversation_id> <root_message_id>` (the root is an item's `parent_message_id`/`reply_chain_id`) or a Teams message link copied from any `link`. Flags: `--limit` (default 50; check `truncated`), `--include-deleted`, `--html`, `--include-system`.

### conversations, teams and people

`teams` lists teams `{team_id, display_name, channel_count, last_activity_at, unread_count}` (plus `tenant_id`, `user_id`), newest activity first, with `--limit` and `--fields`; `unread_count` counts the team's channels too. Its `team_id` or `display_name` is what `--team` takes.

`conversations --kind Chat|Topic|Space|Meeting --query words --team <name|id> [--include-system]` lists conversations, sorted by last activity, newest first (default `--limit 50`, check `truncated`), with `display_name`, `kind`, `member_count`, `last_message_at`, `read_horizon_at`, `favorite`. `people --query name` resolves a name to a `sender_id` for `--from`.

With `--query`, an exact name ranks first, then prefix, then substring, then all-words matches. A chat with no title is named after its other members (`Ana, Ben, Chao +2`) or `Unnamed chat (5 members, id 3fa9c2d1)`; select an untitled chat by `conversation_id`.

### stores and records

Besides messages, conversations and activity, teamscrawl mirrors every other Teams database the app cached (pinned messages, contacts, call history, the raw calendar stores and more) into a generic `records` table. The calendar has its own commands (see Calendar); the rest have no typed command yet. Credential-like databases are never read, and token-shaped values inside other records (JWTs, `access_token`-style fields of any type, `Bearer ` strings, `sig=` URL signatures, credentials inside JSON stored as a string) appear as `[redacted]`. Run `teamscrawl stores` to see what exists: items `{database, store, records, removed, last_updated_at}`, one per database and object store (an account's databases carry its tenant and user ids in the name). Then read one with `teamscrawl records --database Teams:calendar-manager --limit 10 --max-text 300`: `--database` takes the full name or any prefix (required), plus `--store`, `--since`, `--include-removed` and `--limit`. Items are `{source, database, store, key_json, value_json, first_seen_at, updated_at, removed_at?}`, newest change first; `key_json` and `value_json` are parsed JSON, and `value_json` is absent when the value could not be decoded. A record Teams' cache no longer holds keeps its last value and gets `removed_at`, hidden unless `--include-removed`. `--database` prefix matching spans every signed-in account; `--account <tenantId>/<userId>` narrows it (a database whose name carries no account is hidden by `--account`). The `pinned-manager` store holds Teams' own pin records, while `messages` carries a `pinned` flag per message: use whichever answers the question. The shape of each value is Teams' own and can change without notice, so look at one record before filtering on its fields, and keep `--max-text` low.

### sql

One read-only SELECT (or WITH/EXPLAIN/VALUES), for counts and joins the commands do not cover. Returns `{"columns":[...],"rows":[[...]],"count":N,"truncated":bool}`; it streams rows and stops at `--limit`. The word `attach` or `pragma` inside a string literal is fine; only a statement that is not a read is refused (`usage`). Tables: `accounts`, `conversations`, `messages`, `people`, `activity`, `records`, `sync_runs`; FTS tables `message_fts`, `conversation_fts`. Note that `sql` returns every account's rows regardless of `--account`.

### watch

`watch [--every 60s] [--min-interval 60s] [--emit-initial]` runs until interrupted (SIGINT or SIGTERM exit 0) and prints JSON Lines, the one exception to the one-document rule. It syncs once at start as a silent baseline (nothing is printed unless `--emit-initial`), then syncs whenever the Teams cache changes (file events, with a poll every `--every` as the safety net; bursts are debounced) and prints one line per change followed by one report line:

Syncs are spaced at least `--min-interval` (`TEAMSCRAWL_WATCH_MIN_INTERVAL`, `0` disables) apart so a busy cache does not keep a CPU core busy: expect changes up to about a minute plus a sync after Teams wrote them, not instantly.

Example lines: `{"kind":"message","change":"new","item":{...}}`, `{"kind":"activity","change":"edited","item":{...}}`, `{"kind":"sync","report":{...}}`.

When the first sync after an upgrade re-derives an older archive, watch prints one `{"kind":"migrated","from":1,"to":2,"rows":N}` line first; it is not a change.

`change` is `new`, `edited` or `deleted`; `item` has the same shape as a `messages` or `activity` item and honors `--fields`, `--max-text` and `--account` (system pseudo-conversations are skipped). A failed sync prints `{"kind":"error","error":{"code","message","fix"}}` and watching continues. When some sources committed and others failed (`partial_sync`), watch first prints the committed sources' changes and a `sync` line with `"status":"partial"`, then the error line, and the next pass retries only the failed sources, so no change is lost. `--fields html` fills `html`. A locked archive prints a `locked` warning on stderr and retries. Environment errors (`teams_not_installed`, `no_full_disk_access`, `no_teams_origin`, `archive_newer`) end the run with exit 3. Run it in the background and read its stdout; use `--fields` and `--max-text` to keep lines small.

### doctor, sync, status

- `doctor` returns `{"ok":bool,"checks":[{"name","ok","warn"?,"detail","fix"}]}`. A warning (for example `last_sync_age` "never synced", or `last_sync_status` after a partial or failed sync) does not fail it; `archive_newer` fails it when a newer teamscrawl wrote the archive. Follow each failing check's `fix`.
- `sync` returns counts (`seen`, `inserted`, `updated`, `unchanged`) per entity (including `records`), `omissions` by reason, and `status` of `ok`, `ok_with_omissions` or `unchanged` (exit 0), or `partial` when some Teams sources committed and others failed (exit 1: the report, with each source's `status` and `error`, is on stdout and a `partial_sync` error is on stderr; run `teamscrawl doctor`, fix the cause, sync again, since the sources that did sync are already in the archive). The first `sync` after upgrading from alpha.1 re-derives older rows and reports `migrated`; read-only commands never migrate (SPEC.md section 3.2). A sync skips records whose stored bytes did not change since the last sync and reports the same counts a full read would; `sync --full-read` (or `TEAMSCRAWL_FULL_READ=1`) reads every record in full, with the same result (SPEC.md section 4.1).
- `status` shows per-account counts, the last run and other Teams origins seen.

## Calendar

`teamscrawl calendar` answers questions about the user's meetings from the Teams cache and, when it is turned on, the new Outlook for Mac store. Run the commands with `--json`, and read `coverage_gap` and `unknown_fields` before you trust an empty answer.

| Question | Command |
| --- | --- |
| What is on my calendar (default: today)? | `calendar --from tomorrow --days 7`, `calendar --from 2026-10-12 --to 2026-10-19`, `calendar --query planning`. Add `--fields event_id,subject,start,end,sources,has_recap` to keep the answer small (`--fields` and `--max-text` work on `calendar`, `calendar event` and `calendar actions`). `--from` and `--to` take `today`, `yesterday`, `tomorrow`, `YYYY-MM-DD`, RFC3339 or a signed offset (`+3d`, `-1d`). Write a negative offset with an equals sign (`--from=-7d`); `--from -7d` is a usage error. An unsigned `7d` is a usage error too. |
| Who is in it, what is the agenda, the recap, the recording? | `calendar event <event_id>`: take the `event_id` (or `event_key`, or a unique prefix) from the agenda. Add `--max-text 400` to cut long bodies and summaries. |
| What did I agree to do? | `calendar actions --from=-7d --to=tomorrow --mine`. Without `--mine` it lists everyone's items (`--owner pat` filters by name). |
| What does the archive hold, and how fresh is it? | `calendar sources [--account ...]`: one row per account and source, Teams first, with `covered_days`, `last_verified_at`, event and recap counts and `unknown_time_zones`. An Outlook row adds `status` (`ok`; `skipped_interval`, which is not a failure: the last sync reused a read under 5 minutes old; `unsupported_version`, `unsupported_layout` or `unreadable`, which mean Outlook is not being read, with `error.fix`), `last_read_at`, `next_read_after`, `deletions: "unverified"` and `link` (`config` when linked to its `principal`, else `none`). With the Outlook source off, archived Outlook rows still appear but are no longer refreshed. Run it when an answer looks too thin or too old, or to see which days the archive covers (see Choosing a range). |
| Why is Outlook missing or stale? | `doctor`: the `outlook_store` check says whether the source is on, which profiles it found, whether the store version is readable and when the last read ran. It only warns. |

Agenda items are chronological. Cancelled events, events you declined and recurring masters are hidden unless you add `--include-cancelled`, `--include-declined` or `--include-masters`; `--include-removed` adds events a source saw go (`removed: true`, `removed_by` names the sources). Default `--limit` is 50: check `truncated`. To read a meeting's chat, pass the event's `meeting_chat_id` as `-c` to `messages`.

### "Not known" is not "none"

Teams caches an event's attendees, body and rooms only for meetings the user opened, and Outlook does not state every flag. So a missing key can mean "the source never said". The rule:

- A flag the source stated is printed `true` or `false` (`cancelled`, `is_organizer`, `is_private`, `all_day`, `has_attachments`). A flag it did not state has no key and its name is in `unknown_fields`. A missing `cancelled` is "not known", never "no".
- A list or text field (`attendees`, `body`, `rooms`, `attachments`, `categories`) named in `unknown_fields` was never fetched. The same field absent and not named was stated empty.
- `detail_level` is `basic` (schedule only), `full` (attendees, body and rooms from a copy as new as the schedule) or `stale` (the detail is older than the schedule, so the attendee list may be out of date). `detail_as_of` says how old.
- `filled_fields` lists schedule fields taken from the other source or an older copy, each `{field, from, as_of}`. Treat them as true as of `as_of`.
- How a linked twin is merged: Teams owns the Teams meeting fields (join link, dial-in, meeting chat id), and recordings and recaps never detach on link. Attendees and rooms are the union of both copies. The schedule and every other scalar come from the newer copy. `overridden_fields` lists each field whose value differed between the copies and which source won (a field not named there agreed, or was stated by one copy only). The name `overridden_fields` is not final: if the key is missing, read the item's other keys.
- `sources` says who holds the event: `["teams"]`, `["outlook"]` or both.

### Choosing a range

The agenda is today unless you say otherwise. Pick the range from the question: "tomorrow" is `--from tomorrow`, "this week" is `--from today --days 7`, "last week" is `--from=-7d --to=today`, a named day is `--from 2026-10-12 --days 1`. `--days` replaces `--to`. Keep ranges to a week or two: a long range returns many items (check `truncated`) and `uncovered_days`.

To tell empty from unknown, read `coverage_gap` and `uncovered_days`. `coverage_gap: false` means every day was covered, so an empty agenda means no meetings. `coverage_gap: true` with the day you asked about in `uncovered_days` means Teams never cached that day: report it as unknown, not as free. `uncovered_days` is capped at 31 days (`uncovered_days_total` gives the full count), so do not page through it. For a quick look at what the archive covers, run `calendar sources --fields source,account_id,window_start,window_end,covered_days`, or `calendar --days 1 --fields event_id --limit 1` for one day.

### How fresh, and what is missing

- `archive_age_seconds` is the age of the last full sync. Pass `--max-age 15m` for a current answer. Teams changes reach the archive only when a sync reads the cache, and Outlook is read at most once every 5 minutes (a skipped read is `skipped_interval`).
- Teams caches the days the user looked at, not a range. `coverage_gap: true` means some day of the range is not covered, so an absent event is not evidence that there was none; `uncovered_days` names the days (at most 31). `coverage_as_of` is the oldest verification of a covered day, and `accounts[]` gives each account's own `synced_at` and `coverage_as_of`. With several accounts the gap is true when any account lacks a day, so add `--account <tenantId>/<userId>` to judge one.
- A day on which every event was deleted vanishes from the cache and reads as a gap, not as "empty".
- Recaps expire in Teams (`expires_at`); the archive keeps what it saw. A recording or transcript is listed only when its message reached the meeting chat, and the files are never downloaded.

### Outlook

Outlook is off by default. Turn it on for `sync` and the read commands with `--outlook-root DIR` (`TEAMSCRAWL_OUTLOOK_ROOT`) or `TEAMSCRAWL_OUTLOOK=1` (the default macOS directory). Events already archived stay readable without the flag, but only a run with it on refreshes them. An unlinked Outlook profile is its own account: its events are not merged with Teams, and the agenda lists it in `unlinked_accounts` with the exact command to link it in `unlinked_fix`. Linking is explicit and never guessed, because two people invited to one meeting hold the same events:

```sh
teamscrawl whoami --json     # the Teams accounts: tenant_id, user_id, display_name
teamscrawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>
```

Which account to link: nothing the tool prints names the Outlook profile's owner. An Outlook account is only `outlook/<profile directory name>` (in `calendar sources` and `unlinked_accounts`), and `whoami` lists Teams accounts by `display_name` and ids with no email address, so the two cannot be matched by the tool. Do not guess from the display name, and do not link by event overlap (two people invited to one meeting hold the same events). If `whoami` lists one account and the user says it is theirs, use it. Otherwise ask the user which Teams account (by `display_name`) owns the Outlook profile, and ask only once.

`--outlook-profile` is needed only when the root holds several profiles. The link is kept in the archive; `--outlook-account none` ends it.

What you will see: the linking `sync` can print `skipped_interval` for the Outlook source and `linked: 0`. That is not a failure. The link is applied after the sources are read, and an Outlook read under 5 minutes old is reused, so there was nothing new to count. To confirm the link took effect, run `teamscrawl calendar sources --json`: the Outlook row now has `link: "config"` and its `principal` is the Teams account. The agenda confirms it too: `unlinked_accounts` and `unlinked_fix` are gone and twin events have merged, so the agenda gets shorter.

Linked, twins merge into one item with `sources: ["teams","outlook"]` and `filled_fields`. A linked Outlook-only event gets a new `event_id`; the old one still resolves. A meeting Teams marked gone stays hidden unless the Outlook copy was edited after the removal. teamscrawl does not detect deletions in Outlook, so a meeting deleted only in Outlook stays listed.

### What "mine" means

Everything in the agenda is on the signed-in user's calendar, as organizer or invitee (`is_organizer`, `response`). The agenda has no `--mine`. For action items, `--mine` keeps items whose `owner` is the user's display name from `whoami` (`mine_basis: full_name`), or a first name alone that no other attendee of that meeting shares (`first_name`). When someone else in the meeting shares the first name, the item is `ambiguous`: `mine` is absent, `--mine` leaves it out and counts it in `mine_ambiguous_omitted`, so run without `--mine` and read those. Owners written "Last, First" never match. `mine_unknown_accounts` names accounts whose own name is not archived; with no name at all `--mine` is a usage error (sync, or use `--owner`).

### Recaps and action items

A recap is Teams' own meeting summary for a call: `headline`, `short_summary`, `outline`, `action_items` (with `owner`), `mentions`, `speakers`, `topics` and the `recording`. `has_recap` and `action_item_count` on an agenda item say whether `calendar event` has one. Its `link_method` says how it reached the event: `ical_uid` (certain), `time` (start and end within a minute: very likely) or `start_time` (the only event starting within five minutes of the meeting start: weigh it). A recap that no event holds is under `unlinked_recaps` in the agenda, with `placed_by` saying which time placed it. In a recurring series, a recap that cannot be tied to one occurrence has `series_level: true` and appears on each occurrence; `meeting_start` says which one it belongs to. Recordings and transcripts of the meeting chat are in `recordings` (`matched_by` is `recap` or `window`); those that match no occurrence are in `series_recordings`.

## Freshness

- Every read result has `archive_age_seconds`: the age of the last fully successful sync, for the accounts the read covers (`--account X` uses X alone; otherwise the stalest account). A partial or failed sync does not refresh it, and `sync --account X` refreshes only X. If it matters that the answer is current, rerun with `--max-age 5m` or run `teamscrawl sync`.
- If the implicit sync fails, the command still answers from the archive, prints a warning on stderr (`{"warning":{"code","message","fix"}}` in JSON mode) and adds a `sync_error` field to the result. Treat the answer as possibly stale and follow the warning's `fix` if freshness matters.
- Teams evicts old messages from its own cache. The archive keeps them, but only what a sync has seen, so sync regularly (for example `--max-age 1h` in every call).

## Context budget

Messages can be long. Start with `--max-text 300` (sets `text_truncated: true` on cut items; it keeps the first N characters including the trailing `…` and may cut mid-word), `--fields id,sender_name,sent_at,text,link` to drop the rest, and a small `--limit`. Fetch full text later for the few items that matter with `thread` or `messages -c ... --limit 1`. Fields accepted by `--fields` are the item's top-level keys; an unknown key is a `usage` error that lists the valid ones.

## Citing

Every item has a `link`, a Teams deep link (`https://teams.microsoft.com/l/message/<conversationId>/<messageId>?tenantId=...`). Cite it so the user can open the message in Teams. Also give `conversation_display_name`, `sender_name` and `sent_at`.

## Errors

Errors go to stderr as `{"error":{"code","message","fix"}}` and set the exit status. Follow `fix`.

Exit 0 is success, including a `sync` with status `ok_with_omissions` or `unchanged`. This table lists every code; SPEC.md section 6.7 (see above) has the same table with when each is raised.

| Exit | Code | Meaning and action |
| --- | --- | --- |
| 2 | `usage` | Bad flag, filter, `--fields` key, account or query. Fix the command; `--help` shows options. |
| 3 | `teams_not_installed` | The new Teams data directory is missing. Ask the user to install new Teams and sign in. |
| 3 | `no_full_disk_access` | macOS blocked the read. The `fix` names the app (your terminal or agent host) to grant in System Settings > Privacy & Security > Full Disk Access; it must be restarted. Ask the user. Windows should not return this on the default LocalCache path. |
| 3 | `no_teams_origin` | Teams has no cache yet. Ask the user to open Teams and sign in, then retry. |
| 3 | `doctor_failed` | A required `doctor` check failed. Run `teamscrawl doctor` and follow the failing check's `fix`. |
| 3 | `archive_newer` | The archive was written by a newer teamscrawl, so this build refuses to write it (`sync`, `watch` and the implicit sync fail before any write; reads still work, and an implicit sync becomes a warning plus `sync_error`). Run the `fix`: upgrade teamscrawl, or use another `--db`. |
| 4 | `locked` | Another sync is running. Wait and retry. |
| 1 | `snapshot_inconsistent` | Teams was writing while the cache was copied. Retry once; if it persists, ask the user to quit Teams briefly. |
| 1 | `unsupported_block_compression` | The cache uses a format this version cannot read. Update teamscrawl and report it with `doctor` output. |
| 1 | `store_missing` | A Teams store vanished. Ask the user to open Teams until it loads, then retry; if it persists, update teamscrawl. |
| 1 | `db_error` | The archive cannot be read or written. Check `--db` path, permissions and disk space. On Windows, also check that a custom `--db` parent directory and any pre-existing archive file are already private to the current user and SYSTEM. |
| 1 | `partial_sync` | Some Teams sources synced and others failed; the message names each failed source and its code. The synced sources are in the archive. Run `teamscrawl doctor`, fix the cause and sync again. |
| 1 | `interrupted` | The command was stopped (Ctrl-C or a signal) before it finished; nothing was half-written. Run it again. A second Ctrl-C or SIGTERM during the stop quits at once with exit 130 and may leave a temporary snapshot, which a later sync removes once it is older than an hour. |
| 1 | `internal` | A teamscrawl bug. Report the command you ran. |

Recovering from an empty or stale archive: run `teamscrawl sync`, then repeat the read. An empty result after a successful sync means the desktop cache has nothing matching, not that teamscrawl failed.

## Rules
- Never paste message content into places the user did not expect; the archive is private.
- Never run anything that edits Teams' folders or the archive by hand; use the commands.
