# Command reference

This page lists every m365crawl command and flag, with the shape of each result. `m365crawl <command> --help` prints the same flags from the installed binary. The normative behaviour is in [SPEC.md](../SPEC.md).

Start with `m365crawl` (the overview) and `m365crawl --help`. Commands fall into seven groups:

| Group | Commands |
| --- | --- |
| Overview and health | [`m365crawl`](#overview), [`doctor`](#doctor), [`status`](#status), [`whoami`](#whoami), [`sync`](#sync), [`watch`](#watch) |
| Across sources | [`search`](#search), [`people`](#people), [`sql`](#sql) |
| Teams | [`messages`](#messages), [`unread`](#unread), [`thread`](#thread), [`conversations`](#conversations), [`teams`](#teams), [`activity`](#activity), [`stores`](#stores), [`records`](#records) |
| Mail | [`mail list`](#mail-list), [`mail show`](#mail-show), [`mail thread`](#mail-thread), [`mail folders`](#mail-folders), [`mail unread`](#mail-unread) |
| Calendar | [`calendar`](#calendar), [`calendar event`](#calendar-event), [`calendar actions`](#calendar-actions), [`calendar sources`](#calendar-sources) |
| Meeting transcripts | [`transcripts`](#transcripts), [`transcripts show`](#transcripts-show), [`transcripts fetch`](#transcripts-fetch), [`transcripts signin`](#transcripts-signin) |
| Tooling | [`metadata`](#metadata), [`skill`](#skill), [`version`](#version) |

## Platform defaults

| Host | Default `--teams-root` | Default `--outlook-root` | Default `--db` |
| --- | --- | --- | --- |
| macOS | `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView` | `~/Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles` | `~/.m365crawl/m365crawl.db` |
| Windows | `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView` | none: Outlook is not read on Windows yet | `%LOCALAPPDATA%\m365crawl\m365crawl.db` |

On Windows m365crawl reads Teams chats and the Teams calendar. Every `mail` command there fails with `mail_unsupported_platform`: "mail is not yet read on Windows; Teams chats and the calendar work here — try `m365crawl calendar`". `search` on Windows returns chats with a `note` that says mail was left out.

On Windows the default archive path is private by construction. A custom `--db` path is allowed only when its direct parent directory is already private to the current user and SYSTEM, or m365crawl can create that parent itself as a new private directory. If that parent already exists and is not private, or the archive file already exists and is not private, open fails with `db_error` before SQLite writes anything.

## Global flags

Every command accepts these. `--fields` and `--max-text` apply to the list and read commands only (`search`, `messages`, `conversations`, `teams`, `people`, `activity`, `stores`, `records`, `unread`, `thread`, `watch`, every `mail` command, `calendar`, `calendar event`, `calendar actions`, `transcripts` and `transcripts show`); on any other command they are a `usage` error.

| Flag | Meaning |
| --- | --- |
| `--format=text\|json\|log` | Output format: text, json or log. Default: text on a terminal, json otherwise. |
| `--json` | Alias for `--format json`. |
| `--db=PATH` | Archive database path (`$M365CRAWL_DB`). Default: see the table above. |
| `--teams-root=DIR` | Teams EBWebView directory (`$M365CRAWL_TEAMS_ROOT`). Default: the new Teams container. |
| `--outlook-root=DIR` | New Outlook for Mac profiles directory, read for mail and the calendar (`$M365CRAWL_OUTLOOK_ROOT`). Default: the new Outlook's own directory, read when it is there. `none` turns Outlook off, mail included. Outlook is off when `--teams-root` is set without this flag. A default Outlook never fails a sync: a missing directory, no Full Disk Access or an unreadable store is reported (status `unavailable`) and the Teams sync carries on. A named directory that does not exist is the error `outlook_root_missing` (exit 3) on `sync` and on every read command except `whoami`, `status` (which reports it in an `outlook` field) and `version`. |
| `--outlook-account=TENANT/USER\|none` | Link the Outlook profile to this Teams account so their events merge; `none` ends the link and keeps it ended (`$M365CRAWL_OUTLOOK_ACCOUNT`). A profile signed in under a Teams account's own address is linked automatically (method `address`); this flag always wins over that. The link is kept, so the flag is needed only to change it. |
| `--outlook-profile=NAME` | The Outlook profile `--outlook-account` applies to. Required when more than one profile is under the Outlook root (`$M365CRAWL_OUTLOOK_PROFILE`). |
| `--account=ACCOUNT` | Only this account. Teams commands take a Teams account, `<tenantId>/<userId>` (`whoami` lists them); `mail` commands take a mail account, `outlook/<profile>`; `search` takes either (see [search](#search)). Default: every account. |
| `--no-color` | Disable colored output (also `NO_COLOR`). `CLICOLOR_FORCE=1` forces color. |
| `--max-age=DURATION` | Read commands sync first when the last successful sync is older than this (for example 15m, 2h, 1d); `0` disables the implicit sync (`$M365CRAWL_MAX_AGE`). When a read syncs first, stderr gets one line before it starts: in text mode `m365crawl: syncing — archive is 2h14m old (max-age 15m)`, in json and log mode `{"notice":"syncing","reason":"stale","archive_age_seconds":N,"max_age_seconds":N}`. The result then gains `synced: {seconds, status}`. |
| `--fields=a,b,c` | Keep only these top-level keys of each item, comma separated. An unknown key is a `usage` error. Each list command's `--help` ends with the keys it accepts, and each command below lists them under "`--fields` keys". |
| `--max-text=N` | Truncate each item's text to N characters, including the trailing `…`, and set `text_truncated`. `0` keeps all of it. |
| `--version` | Print the version, commit and build date, then exit. |

## Output

Output is JSON when stdout is not a terminal and text on a terminal. In JSON mode each command prints exactly one document; `watch` is the one exception and prints JSON Lines.

- **Lists** are `{"items": [...], "count": N, "truncated": bool}`. `--limit` defaults to 50. A truncated list also carries `total` when the exact count is known.
- **`note`** is one plain sentence on an empty list, for Teams, calendar and mail lists alike. It says why the list is empty: no archive yet, no data of that source yet, a time range outside what the archive holds (naming the window), filters that matched nothing, a source this operating system does not read, or an `--account` the archive does not hold. A list with items has no `note`, except where a command documents its own use: `search` (a source a flag left out), `mail list --unread`, `mail unread` and `mail thread`.
- **Freshness.** Every read result carries `archive_age_seconds`, counted from the last fully successful sync of the accounts it covers. An archive with no complete sync adds `"needs_sync": true` and `"hint": "run m365crawl sync"`.
- **Errors** go to stderr as `{"error": {"code", "message", "fix"}}`, and `fix` is an instruction you can follow. Exit codes: 0 success, 1 runtime failure, 2 usage, 3 environment not ready, 4 another run holds the lock. [SPEC.md](../SPEC.md) section 6 lists every error code.

## Overview

`m365crawl` with no arguments prints the overview: what the archive holds and how fresh it is, then the "Start here" block.

```
m365crawl
```

In text mode it prints the banner, one row per source with its state, what it holds and its last sync, any source notes, and then the "Start here" block. Per source it shows:

- **chats:** message and conversation counts and the newest message.
- **mail:** message count, the oldest cached and the newest received message.
- **calendar:** event count, the window from the earliest to the latest covered day, and whether today is fully covered (`coverage_gap`).

`--json` gives `{"archive_path", "archive_exists", "sources": [...], "next": [...], "note"?}`. Each source is `{"source": "chats"|"mail"|"calendar", "state", "conversations"?, "messages"?, "events"?, "newest_at"?, "oldest_at"?, "window_start"?, "window_end"?, "coverage_gap"?, "last_sync_at", "note"?}`. `state` is `ok`, `empty`, `no_archive`, `unsupported` (a source this operating system does not read, such as mail on Windows), `off` (the Outlook source is off for this run) or `no_profile` (no new Outlook profile on this machine). `next` lists the "Start here" commands that apply on this operating system, each as `{"command", "does"}`.

The overview opens the archive read-only and never runs the implicit sync. With no archive yet it prints "no archive yet; run m365crawl sync" and the "Start here" block, and exits 0. A word that is not a command is the `usage` error `unknown command "<word>"`, with the closest command when there is one, and the error's `fix` then names that command's `--help`. A likely synonym counts as close: `chats` suggests `conversations`, `email` suggests `mail`, `events` and `meetings` suggest `calendar`, and `transcript` suggests `transcripts`. The synonym itself is never accepted.

`m365crawl --help` starts with the same block:

```
Start here:
  m365crawl sync              read Teams, Outlook mail and the calendar into the archive
  m365crawl                   what the archive holds and how fresh it is
  m365crawl search "words"    find anything across chats and mail
  m365crawl calendar          today's meetings; calendar event <id> for one meeting with its chat and mail
  m365crawl mail unread       unread mail by folder
  m365crawl unread            unread Teams chats
  m365crawl mail list --has-attachments   mail with files; mail show <id> lists each file's name, size and type
```

## doctor

Check that every prerequisite and the archive are ready.

```
m365crawl doctor [flags]
```

No flags beyond the global ones.

Result: `{"ok", "checks": [{"name", "ok", "warn"?, "detail", "fix"}]}`. Exit 3 (`doctor_failed`) when a required check fails; warnings do not fail it. Does not run an implicit sync.

| Check | What it says |
| --- | --- |
| `teams_installed`, `full_disk_access`, `teams_origin` | The Teams cache exists and can be read. See [full-disk-access.md](full-disk-access.md). On Windows `full_disk_access` is `ok: true` with detail `not applicable on Windows; Teams cache is under LocalCache, not TCC-protected.` |
| `archive_newer` | Fails when a newer m365crawl wrote the archive. |
| `archive_upgrade`, `last_sync_status` | Warnings about the archive and the last sync. |
| `calendar_cache` | Warns when an account has no Teams calendar in the archive or the newest Teams calendar cache is more than 7 days old. |
| `outlook_store` | Says the Outlook source is off, or lists each profile with whether this build reads its store version, what the last sync recorded and how it is linked to Teams. No Outlook on the machine is a plain pass. It warns when a named root has no profile, access is denied, a store version is unknown or the last read failed, with the same fix a sync gives. |
| `mail_readable` | A warning. Per Outlook profile, when its mail was last read or why the last read failed; also that mail is not read when the Outlook source is off or on Windows. |
| `mail_archive_mode` | Warns when other users can read the archive file, which holds mail. The file is created with mode 0600. Not applicable on Windows. |
| `transcripts_browser` | Names the browser `transcripts fetch` would run: the one `M365CRAWL_BROWSER` names, else Microsoft Edge, then Google Chrome. Doctor only looks for it and never starts it. No browser is a warning (`none: transcripts fetch needs Edge or Chrome`, or, when `M365CRAWL_BROWSER` is set, that it names no browser on this machine), never a failure: every other command works without one. |
| `transcripts_profile_mode` | Warns when other users can read m365crawl's browser profile (`browser` beside the archive), which holds that browser's own sign-in state. Doctor reads the directory's permissions (its mode, or its access list on Windows) and never opens a file in it. No profile yet is a plain pass. |

In text mode the checks are followed by a Snapshot of the archive, one line per source, then the last sync and the archive's age:

- `Teams`: accounts, chats, channels, teams, meeting chats, messages and people, plus recordings and transcripts when the archive holds any. Each is counted once across accounts, without Teams' notification, call-log and annotation feeds or deleted messages.
- `Mail`: messages, unread and folders, once mail has been read.
- `Calendar`: the live events of each source, and a window from the earliest to the latest day any source covers.

The JSON result does not carry the Snapshot.

```sh
m365crawl doctor
```

## status

Show archive counts per account, the last sync, other Teams origins seen, mail and meeting transcripts.

```
m365crawl status [flags]
```

No flags beyond the global ones.

Result: archive path, schema version, per-account counts, the last run and other Teams origins seen, and a `mail` block: `{"messages", "unread", "folders", "oldest_at", "synced_at", "state"}`. `messages` counts mail that is not gone or evicted. `state` is `ok` once a sync has read mail, `skipped` when the Outlook source is off for this run or no sync has read mail yet, `no_profile` when this machine has no Outlook profile, and `unsupported_platform` on Windows.

A `transcripts` block counts the recorded meetings: `{"calls", "parts", "fetchable", "fetched", "last_fetch_at"}`. `parts` leaves out the placeholder of a call that has a transcript notice and no file reference; `fetchable` counts the parts a fetch can ask for and `fetched` those whose text is in the archive; `last_fetch_at` is the newest fetch attempt, null when none ever ran. The block is absent with no archive or an archive from before the transcript tables.

```sh
m365crawl status
```

## whoami

Show the accounts in the archive and the archive's state.

```
m365crawl whoami [flags]
```

No flags beyond the global ones. Result: the accounts in the archive (with `self_id`) and the archive's state.

## sync

Read Teams, Outlook mail and the calendar into the archive once and print what changed.

```
m365crawl sync [flags]
```

| Flag | Meaning |
| --- | --- |
| `--full-read` | Read every record in full, even from a cache that has not changed since the last sync, instead of skipping the records whose bytes are unchanged. The archive comes out the same either way; this is a check, not a repair (`$M365CRAWL_FULL_READ`). |

Result: the sync report (SPEC.md sections 4 and 5), with one entry per source in `sources`. A Teams source carries counts for `conversations`, `messages`, `people`, `activity`, `records` and `calendar` (`events`, `recaps`, `recap_items`, `gone`, `linked`, `refused` and the link counts). Each Outlook profile has a calendar source and a mail source (`outlook|<profile>|mail`); the mail source's counts are `mail: {added, updated, replaced, gone, evicted, withheld}`. A mail failure does not fail the calendar or Teams, and on Windows the mail source is a note carrying `mail_unsupported_platform`, not a failure.

Exit 0 for `ok`, `ok_with_omissions` and `unchanged`. Status `partial` (some sources committed, others failed) prints the report on stdout, a `partial_sync` error on stderr and exits 1. `--account` limits the run to one account and skips the unchanged shortcut.

`--outlook-account` and `--outlook-profile` link an Outlook profile to a Teams account (SPEC.md section 4.2). The link is applied after the sources are read; a refused link exits 2 with the report on stdout, and `--account` cannot be combined with it. Nothing in the output says which Teams account owns an Outlook profile (an Outlook account is `outlook/<profile name>`, and `whoami` has no email), so the caller must know it or ask. Because the link is applied after the read, the report can show the Outlook source as `skipped_interval` (a read under 5 minutes old was reused) and `linked: 0` while the link took effect. Confirm with `calendar sources`: its Outlook row then has `link: "config"` (or `"address"` when the sync linked it by an address signed in to the profile) and the Teams account as `principal`.

```sh
m365crawl sync
m365crawl sync --json
m365crawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>
```

## watch

Stream one JSON line per new, edited or deleted Teams message or activity item as Teams writes its cache. Runs until interrupted.

```
m365crawl watch [flags]
```

| Flag | Meaning |
| --- | --- |
| `--every=DURATION` | Poll interval: the safety net when file events are missed. Syncs run only when the cache changed. Default `60s`. |
| `--min-interval=DURATION` | Least time between the end of one sync and the start of the next. A busy Teams cache changes constantly, so without a pause `watch` would sync back to back. Changes that arrive meanwhile are coalesced into one sync. Default `60s`; `0` disables the pause (`$M365CRAWL_WATCH_MIN_INTERVAL`). |
| `--emit-initial` | Also emit the first sync's changes. By default that sync is a silent baseline and only later changes are emitted. |

Result: JSON Lines, the one exception to the one-document rule (SPEC.md section 7). Line kinds: `message`, `activity`, `sync` (one per sync, with the report), `migrated` (when an upgrade re-derives an older archive) and `error`. Honours `--account`, `--fields` and `--max-text`; system pseudo-conversations are skipped. Exits 0 on SIGINT or SIGTERM.

```sh
m365crawl watch --every 30s --fields id,type,text --max-text 200
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `type`, `subtype`, `is_read`, `at`, `message_id`, `app_id`, `message_sent_at`, `actor_id`, `actor_name`, `actor_inferred`.

## search

Full-text search over Teams chats, Outlook mail and fetched meeting transcripts, newest first; default `--limit 50` (check `truncated`).

```
m365crawl search [<query>] [flags]
```

| Argument | Meaning |
| --- | --- |
| `[<query>]` | Words to find; "quoted phrases" and a trailing `*` for prefixes are supported. Optional when a filter (`--mentions-me`, `--direct-mentions`, `--from`, `--conversation`, `--team`, `--folder`, `--since`, `--until`) is given: then the filters alone select the messages. |

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--source=chats\|mail\|transcripts\|all` | every source | What to search: `chats` (Teams), `mail` (Outlook), `transcripts` (fetched meeting transcripts) or `all` (every one, the default). |
| `--from=STRING` | every source | Sender. Chats: a person id or a case-insensitive part of the name. Mail: a case-insensitive part of the sender's name or address. Transcripts: a case-insensitive part of the speaker's name. |
| `--since=STRING` | every source | Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). For transcripts, the time of each entry. |
| `--until=STRING` | every source | Only messages at or before this time (same formats as `--since`). |
| `--limit=50` | every source | Maximum items in the merged list; `truncated` says whether any source had more. |
| `--folder=NAME\|KIND` | mail | Only mail in this folder, by name or kind (`inbox`, `sent`, …). |
| `-c, --conversation=STRING` | chats | Conversation id, its exact title or display name, or a Teams link to it (a channel, chat, message or meeting link). |
| `--team=STRING` | chats | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--mentions-me` | chats | Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see `mention_kind`). |
| `--direct-mentions` | chats | Only messages that mention you by name (`mention_kind` `person`). |
| `--include-system` | chats | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations). |
| `--include-deleted` | chats | Also search deleted Teams messages. Gone and evicted mail is never searched. |
| `--html` | chats | Add each Teams message's HTML body as `html`. |

How the sources combine:

- Every item has `source`: `chats`, `mail` or `transcripts`. A chat item has the shape of a `messages` item; a mail item has the shape of a `mail list` item.
- A transcript item is one part of a recorded meeting whose fetched text matched: `{"source", "call_id", "event_key", "title", "ordinal", "speaker", "at", "text", "matches", "fetched_at"}`. `speaker`, `text` and `at` (absolute) are those of the part's first matching entry, and `matches` counts the part's matching entries, so a part that says a word fifty times is one item. The words match what was said, not the speaker's name (that is `--from`). Only text already fetched into the archive is searched, offline; with `--source transcripts` and nothing fetched, `note` says `no transcripts are fetched yet; run m365crawl transcripts to see what can be fetched`.
- Text output has a `thread` column that names each item's whole thread: for a chat message the two arguments of `m365crawl thread`, `<conversation_id> <reply_chain_id, else id>`; for mail the id that `m365crawl mail thread` takes; for a transcript the call id that `m365crawl transcripts show` takes.
- Items are newest first across every source, by `sent_at` for chats, `received_at` for mail and `at` for transcripts.
- The result's `sources` says, per source searched, how many items it gave and whether it had more: `{"chats": {"count", "truncated"}, "mail": {"count", "truncated"}, "transcripts": {"count", "truncated"}}`.
- With `--source all`, a flag that belongs to one source narrows the search to that source and `note` says so, for example `--mentions-me applies to Teams chats only; mail and meeting transcripts were not searched`. With another `--source`, the same flag is the usage error `flag_source_conflict`, which names the flag.
- `note` also says when mail is left out because it is not in the archive yet (`mail is not in the archive yet; run m365crawl sync`) or not read on this system.
- `--account` takes a Teams account (`<tenantId>/<userId>`), which narrows chats and searches all mail, or a mail account (`outlook/<profile>`), which narrows mail and skips chats and transcripts. The other source's flag with it, or a `--source` it rules out, is a `flag_source_conflict`.
- `--fields` accepts the keys of every kind of item, plus `source`.
- A query with a syntax error (an unbalanced quote) is one usage error, not one per source.

```sh
m365crawl search planning --limit 5
m365crawl search "budget review" --source mail --since 30d
m365crawl search --mentions-me --since 7d --max-text 200
m365crawl search "release date" --source transcripts --from ada
```

`--fields` keys: `source`, `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `account`, `folder`, `folder_kind`, `to_me`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `received_at`, `is_read`, `flag`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `call_id`, `event_key`, `title`, `ordinal`, `speaker`, `at`, `matches`, `fetched_at`.

## people

List people, newest seen first: Teams senders and members, and Outlook mail senders and recipients (one row per address, id `mail:<address>`).

```
m365crawl people [flags]
```

| Flag | Meaning |
| --- | --- |
| `--query=STRING` | Part of a display name or mail address, or an exact person id. A mail correspondent's id is `mail:<address>`, and a `--query` of that form matches that one address exactly. |
| `--limit=50` | Maximum items in the merged list; `truncated` is true when either source had more. |

Result: a list of person items, newest `last_seen_at` first across both sources. Each carries `tenant_id`, `id`, `display_name`, `sources` and `last_seen_at`.

- **A Teams person** has `sources: ["chats"]` and no `email`, because Teams keeps no address for a person.
- **A mail correspondent** is a sender or recipient with an address; gone messages do not count. It has `id: "mail:<address>"`, `email` (the address, lower case), `sources: ["mail"]`, an empty `tenant_id`, and as `display_name` the newest name it was given, or the address when it never had one.
- **The two are never merged**, so the same person can appear once per source.
- `--account` narrows the Teams people only.

Use a Teams person's `id` or name with `--from` on Teams commands, and a mail address with `--from` on `mail list`.

```sh
m365crawl people --query alex
m365crawl people --query @example.com
```

`--fields` keys: `tenant_id`, `id`, `display_name`, `email`, `sources`, `last_seen_at`.

## sql

Run one read-only SQL query against the archive.

```
m365crawl sql <query> [flags]
```

| Argument or flag | Meaning |
| --- | --- |
| `<query>` | One SELECT (or WITH, EXPLAIN or VALUES) statement. |
| `--limit=50` | Maximum rows to return; the query stops there and `truncated` says whether more rows exist. |

Result: `{"columns", "rows", "count", "truncated"}`. It stops reading at `--limit`, so there is no `total`. Returns every account's rows regardless of `--account`. The mail tables are `mail_messages`, `mail_folders`, `mail_recipients`, `mail_attachments` and `mail_coverage`.

```sh
m365crawl sql "select kind, count(*) as n from conversations group by kind"
m365crawl sql "select kind, count(*) as n from mail_folders group by kind"
```

## messages

List Teams messages in time order (oldest first; with `--limit`, the newest matches); default `--limit 50` (check `truncated`). Teams chats and channels only; for Outlook mail use `m365crawl mail list`.

```
m365crawl messages [flags]
```

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, its exact title or display name, or a Teams link to it (a channel, chat, message or meeting link). |
| `--from=STRING` | Sender: a person id, or a case-insensitive part of the name. |
| `--since=STRING` | Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). |
| `--until=STRING` | Only messages at or before this time (same formats as `--since`). |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |
| `--include-deleted` | Also list deleted messages. |
| `--mentions-me` | Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see `mention_kind`). |
| `--direct-mentions` | Only messages that mention you by name (`mention_kind` `person`), not channel, team, tag or @everyone broadcasts. |
| `--unread` | Only unread messages (chats and meetings unless `--include-channels`). |
| `--include-channels` | Include channels. Off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`. |
| `--html` | Add each message's HTML body as `html`. |

Result: a list of message items, oldest first. Each carries a `link` (a Teams deep link). Text output has a `thread` column with the two arguments of `m365crawl thread` for the message's thread: `<conversation_id> <reply_chain_id, else id>`. Channel thread roots carry `reply_count` and `last_reply_at`; items that mention you carry `mention_kind` (`person`, `channel`, `team`, `tag`, `everyone` or `other`).

```sh
m365crawl messages -c "Fixture team 1 › General" --since 2023-11-14 --max-text 80
m365crawl messages --unread --include-channels --limit 5 --fields id,sender_name,text
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`.

## unread

List unread Teams messages (chats and meetings unless `--include-channels`), newest first; `--by-conversation` gives per-conversation counts. For Outlook mail use `m365crawl mail unread`.

```
m365crawl unread [flags]
```

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, its exact title or display name, or a Teams link to it (a channel, chat, message or meeting link). |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. |
| `--since=STRING` | Count only unread messages sent at or after this time. Use it for "what needs my attention": old read markers leave stale conversations with hundreds of unread messages. |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |
| `--html` | Add each message's HTML body as `html`. |
| `--include-channels` | Include channels (off by default; see `messages`). |
| `--by-conversation` | One item per conversation with its unread count, oldest and newest unread time and a link, most unread first. Ignores `--html`. |
| `--include-system` | Also include Teams' system pseudo-conversations. |

Result: a list of message items, newest first; with `--by-conversation`, one overview item per conversation. Results carry `channels_excluded: true` when channels are left out.

```sh
m365crawl unread --limit 5
m365crawl unread --by-conversation --since 7d
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`. With `--by-conversation`: `conversation_id`, `conversation_display_name`, `kind`, `unread_count`, `oldest_unread_at`, `newest_unread_at`, `link`.

## thread

Show one Teams thread: `<conversation> <root-message-id>`, or a Teams message link. For Outlook mail use `m365crawl mail thread`.

```
m365crawl thread <target> [<root>] [flags]
```

| Argument or flag | Meaning |
| --- | --- |
| `<target>` | Conversation id, or a Teams message link (a channel or chat link names no message: read it with messages --conversation). |
| `[<root>]` | Root message id (not needed with a link). |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |
| `--include-deleted` | Also show deleted messages. |
| `--html` | Add each message's HTML body as `html`. |
| `--include-system` | Also include Teams' system pseudo-conversations. |

Result: a list of message items, root first.

```sh
m365crawl thread 19:topicchannel1@thread.tacv2 1700000045000
m365crawl thread "https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?parentMessageId=1700000045000"
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`.

## conversations

List Teams conversations by last activity, newest first; default `--limit 50` (check `truncated`).

```
m365crawl conversations [flags]
```

| Flag | Meaning |
| --- | --- |
| `--kind=STRING` | Only this kind, for example Chat, Topic (channel), Space (team) or Meeting. |
| `--query=STRING` | Find conversations by name, best match first: an exact name, then names that start with the query, then names that contain it, then conversations whose title holds all the words; ties go newest first. |
| `--team=STRING` | Only this team's own conversation and its channels. |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations. |

Result: a list of conversation items, newest activity first (best match first with `--query`). Untitled group chats are named after their members (`Ana, Ben, Chao +2`). A Meeting conversation that calendar events name as their chat also carries `calendar_series_key` (the series those events share; absent when they belong to several series or are all single events) and `calendar_event_count` (how many live, non-master occurrences of the account hold this chat; a cancelled one is not counted, a declined one is). Go from the chat to its events with `m365crawl calendar --query <subject>`.

```sh
m365crawl conversations --kind Space
m365crawl conversations --query planning
```

`--fields` keys: `tenant_id`, `user_id`, `id`, `kind`, `title`, `display_name`, `team_id`, `member_count`, `last_message_at`, `read_horizon_at`, `favorite`, `calendar_series_key`, `calendar_event_count`.

## teams

List Teams teams with their channel count, last activity and unread count. A team's `team_id` or `display_name` is what `--team` takes.

```
m365crawl teams [--limit N]
```

Result: a list of `{tenant_id, user_id, team_id, display_name, channel_count, last_activity_at, unread_count}`, newest activity first. `last_activity_at` is the newest message time across the team and its channels, and `unread_count` counts unread messages in its channels (whatever `--include-channels` says elsewhere).

```sh
m365crawl teams --fields team_id,display_name,unread_count
```

`--fields` keys: `tenant_id`, `user_id`, `team_id`, `display_name`, `channel_count`, `last_activity_at`, `unread_count`.

## activity

List Teams activity-feed items (mentions, replies, reactions) with their messages.

```
m365crawl activity [flags]
```

| Flag | Meaning |
| --- | --- |
| `--unread` | Only unread items. |
| `--type=STRING` | Only these activity types, comma separated, matched exactly in any case. Seen in the cache: mention (you were @-mentioned in a channel, as a team or tag), mentionInChat (in a chat, or by @everyone), reply, replyToReply, follow, reaction, reactionInChat, msGraph (system notices such as meeting updates and approvals), teamMembershipChange, threadActivity. |
| `--team=STRING` | Only items in this team and its channels. |
| `--direct-mentions` | Only mention items that name you, not channel, team, tag or @everyone mentions. |
| `--since=STRING` | Only items at or after this time. |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations. |

Result: a list of activity items joined with their messages. `actor_id` and `actor_name` say who did it, which can differ from `sender_*` (the related message's author): the reactor for `reaction` and `reactionInChat` (inferred from the reaction nearest in time, so flagged `actor_inferred: true` and possibly wrong when several people reacted at once); the message's sender for `mention`, `mentionInChat`, `reply`, `replyToReply` and `follow`; omitted for `msGraph`, `teamMembershipChange` and `threadActivity`, and when the message is not archived.

```sh
m365crawl activity --unread --limit 5 --fields at,type,conversation_display_name,text
m365crawl activity --type mention,mentionInChat
```

`--fields` keys: `tenant_id`, `user_id`, `id`, `type`, `subtype`, `is_read`, `at`, `conversation_id`, `conversation_display_name`, `message_id`, `reply_chain_id`, `app_id`, `sender_id`, `sender_name`, `message_sent_at`, `text`, `text_truncated`, `link`, `actor_id`, `actor_name`, `actor_inferred`.

## stores

List every Teams database and object store archived without a typed table, with record counts. The database name is what `records --database` takes.

```
m365crawl stores [flags]
```

Result: a list of `{database, store, records, removed, last_updated_at}`, sorted by database and store, never truncated. `records` counts the live rows and `removed` the rows Teams' cache no longer holds. `--account` hides databases whose name carries no account.

`--fields` keys: `database`, `store`, `records`, `removed`, `last_updated_at`.

## records

List the archived records of one Teams database (or a prefix of its name), newest change first; default `--limit 50` (check `truncated`).

```
m365crawl records --database=STRING [flags]
```

| Flag | Meaning |
| --- | --- |
| `--database=STRING` | Database name or a prefix of it, for example `Teams:calendar-manager`. See `stores` for the names. |
| `--store=STRING` | Only this object store, matched exactly. |
| `--since=STRING` | Only records changed at or after this time. A removal counts as a change. |
| `--include-removed` | Also include records Teams' cache no longer holds (they keep their last value and have `removed_at` set). |
| `--limit=50` | Maximum items to return. |

Result: a list of `{source, tenant_id?, user_id?, database, store, key_json, value_json?, first_seen_at, updated_at, removed_at?, text_truncated?}`. `key_json` and `value_json` are parsed JSON, not strings; `value_json` is omitted when the value never decoded. `--max-text N` turns a longer `value_json` into a JSON string of its first characters.

```sh
m365crawl records --database Teams:pinned-manager --store pins --since 7d
```

`--fields` keys: `source`, `tenant_id`, `user_id`, `database`, `store`, `key_json`, `value_json`, `first_seen_at`, `updated_at`, `removed_at`, `text_truncated`.

## mail

The `mail` commands read Outlook for Mac mail from the archive. Mail comes from Outlook's local cache, so the archive holds what Outlook has cached plus what earlier syncs kept. Every result says when mail was last synced and how far back the cache reaches.

- **Ids.** A message id is `<account>:<detail_key>`, split on the last colon, as `mail list` prints it (for example `outlook/Main:12345`). `--account` takes the mail account as it appears in the ids (`outlook/<profile>`), not a Teams account.
- **List results** are `{"items": [...], "count", "truncated", "synced_at", "coverage": [{"account", "folder", "kind", "oldest_at", "newest_at", "count"}], "note"}`. `note` explains an empty result (no archive yet, Outlook source off, no Outlook profile, mail not read yet, filters matched nothing, or the cache covers only since a date). For unread results it says how many messages the cache holds in each folder, for example "the local cache holds 17 messages in Inbox; Outlook may show more", or "no unread mail in the cache; Outlook may show more". Text output of every `mail` list ends with `synced <time> · cache covers since <date>`.
- **Limits.** `--limit` is at most 1000 on every `mail` command; a larger value is a `usage` error.
- **Missing folders.** A message whose folder Outlook's store no longer holds is kept, with `folder` null and `folder_kind` `unknown`. `--folder unknown` lists those messages.
- **Not known is null.** A field the archive does not hold is `null`, never guessed: `subject`, `from_name`, `from_address`, `in_reply_to`, `ical_uid` and `internet_message_id` when Outlook stored none; `is_read` when the read state is not known; `sent_at` when the store has no send time; `address` on a recipient without one; and `gone_at` and `evicted_at` while the message is present.
- **Recipients.** To and Cc are not yet told apart. A recipient carries only `name`, `address` and `kind_raw` (the store's own number); there is no `to` or `cc` field.
- **Importance** (`low`, `normal`, `high`) is likely right, but it is read from one store field that is not fully confirmed.
- **Gone and evicted.** A message a sync saw disappear inside the cache's covered range is `gone`; one older than the covered range is `evicted`, because Outlook dropped it from the cache. Both stay in the archive and are hidden unless you ask for them.
- **On Windows** every `mail` command fails with `mail_unsupported_platform` (exit 3).

### mail list

List archived Outlook mail, newest first.

```
m365crawl mail list [flags]
```

| Flag | Meaning |
| --- | --- |
| `--folder=NAME\|KIND` | Only this folder, by name (checked first, ignoring case) or kind: `inbox`, `sent`, `drafts`, `archive`, `deleted`, `to_me`, `other`. An unknown folder is the `unknown_folder` error, which lists the folders; for `junk` it says junk folders are not detected by kind and to pass the junk folder's name instead. |
| `--from=TEXT` | Only messages whose sender name or address contains this text, ignoring case. |
| `--since=DATE` | Only messages received at or after this time (YYYY-MM-DD, RFC3339 or an age such as `7d`). |
| `--until=DATE` | Only messages received before this time; a date alone (YYYY-MM-DD) includes that whole day, a time is exclusive. |
| `--unread` | Only unread messages. |
| `--flagged` | Only flagged messages. |
| `--has-attachments` | Only messages with attachments. |
| `--include-gone` | Also list messages a sync saw disappear. |
| `--include-evicted` | Also list messages that fell out of the cache's covered range. |
| `--limit=50` | Maximum messages to return (1 to 1000); `truncated` says whether more exist. |

Filters combine with AND. Item keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview` (the first three names), `in_reply_to`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`.

```sh
m365crawl mail list --folder inbox --unread
m365crawl mail list --from pat --since 7d --fields id,subject,from_name,received_at
m365crawl mail list --has-attachments --limit 20
```

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

### mail show

Show one message: headers, recipients, attachments and body text.

```
m365crawl mail show <id>
```

Keys are those of `mail list`, with `recipients` (every recipient, each `{name, address, kind_raw}`) in place of the count and preview, plus `read_state`, `attachments` (each `{name, size, content_type, inline, downloaded}`), `body_text` and `body_state`. `content_type` comes from the file extension. `inline: true` marks an embedded item, which does not count toward `has_attachments`; `downloaded` says whether Outlook's cache holds the file. `--max-text N` cuts `body_text` and sets `text_truncated`. A malformed id is `bad_mail_id`; an id the archive does not hold is `mail_not_found`; an `--account` that differs from the id's account is `account_mismatch` (the id already names the account, so `--account` is not needed).

```sh
m365crawl mail show outlook/Main:12345 --max-text 2000
```

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipients`, `in_reply_to`, `received_at`, `sent_at`, `is_read`, `read_state`, `flag`, `importance`, `has_attachments`, `attachments`, `preview`, `body_text`, `body_state`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

### mail thread

Show the conversation a message belongs to: its reply chain, or messages with the same subject and a shared participant when Outlook kept no reply link.

```
m365crawl mail thread <id>
```

Items have the shape of `mail list`, oldest first. `grouping` is `reply_chain` (In-Reply-To and Message-ID links followed in both directions) or `subject` (the same subject, ignoring `Re:`, `Fw:` and similar prefixes, with at least one shared participant). `participants` lists each person in the thread once (`name`, `address`). When nothing shares the message's reply chain or subject, the thread is the message alone and `note` says so. `mail thread` takes the same ids as `mail show` and returns the same id errors.

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

### mail folders

List mail folders with message and unread counts and how far back the cache reaches.

```
m365crawl mail folders
```

Item keys: `account`, `folder`, `kind`, `messages`, `unread`, `oldest_at`, `newest_at`, `read_at`. Counts leave out gone and evicted messages. `kind` is `inbox`, `sent`, `drafts`, `archive`, `deleted`, `to_me` or `other`. Folders you created are `other`, and so are the system folders the store does not tell apart, Junk Email among them: m365crawl does not detect junk mail. `to_me` is the folder that shares messages with the Inbox; when it shares none it is `other`.

`--fields` keys: `account`, `folder`, `kind`, `messages`, `unread`, `oldest_at`, `newest_at`, `read_at`.

### mail unread

Unread mail by folder.

```
m365crawl mail unread [--folder NAME|KIND] [--limit N]
```

`folders` holds one `{account, folder, kind, unread, cached}` per folder that holds messages. `unread_total` is the sum of the folders' `unread`, every folder included. `items` are the newest unread messages (`--limit`, default 20). `cached` is what the archive holds in the folder; Outlook may show more.

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

## calendar

Read the calendar offline: the agenda for a range (default today), merged across Teams and Outlook. The Teams cache holds the days Teams has loaded, so `coverage_gap` says when part of the range is not covered; an absent event is then not evidence that there was none.

```
m365crawl calendar [flags]
m365crawl calendar event <event> [flags]
m365crawl calendar actions [flags]
m365crawl calendar sources [flags]
```

Flags of the agenda:

| Flag | Meaning |
| --- | --- |
| `--from=WHEN` | Start of the range: today, yesterday, tomorrow, YYYY-MM-DD (midnight in this machine's zone), RFC3339, or a signed offset from now (+3d, -1d, +2w, +90m). Default: today. An unsigned duration such as 7d is a usage error. Write a negative offset with an equals sign, `--from=-7d`: `--from -7d` is a usage error. |
| `--to=WHEN` | End of the range, exclusive; same forms as `--from`. Default: the start of the next day. Not with `--days`. |
| `--days=INT` | Range length in days from `--from` (instead of `--to`). |
| `--query=STRING` | Only events whose subject, organizer or location contains this text, ignoring case. |
| `--include-cancelled` | Also list cancelled events. |
| `--include-declined` | Also list events you declined. |
| `--include-masters` | Also list recurring masters, which stand for the whole series. |
| `--include-removed` | Also list events a source saw go (`removed` is true on them, `removed_by` names the sources). |
| `--limit=50` | Maximum items to return; `truncated` says whether more exist. |

Result of the agenda: `{"items", "count", "truncated", "total"?, "coverage_gap", "coverage_as_of"?, "uncovered_days"?, "uncovered_days_total"?, "accounts"?, "range": {"from", "to", "zone"}, "unlinked_accounts"?, "unlinked_fix"?, "unlinked_recaps"?, "unlinked_recaps_total"?, "notices"?, "note", "archive_age_seconds"}`.

- `coverage_gap` is true when some day of the range is not covered by any cached data. `coverage_as_of` is the oldest time a covered day of the range was verified. `uncovered_days` lists the dates some account does not cover (at most 31; `uncovered_days_total` gives the count when it is longer).
- `accounts` lists, per account (an unlinked Outlook profile is its own), `account_id`, `synced_at` and its own `coverage_as_of`, so an agent can tell which account to distrust. When several accounts are in scope, an entry has `"uncovered_all": true` when it lacks every day of `uncovered_days`, or its own `uncovered_days` when it lacks fewer. An entry with neither covers the whole range. An account with a Teams calendar also has `teams_cache_fresh_at`: when the Teams calendar cache last synced with the calendar service (the `cache_fresh_at` of its Teams row in `calendar sources`). The agenda is read from that cache, so an event created or changed after this time, such as an invitation sent a moment ago, is not listed until Teams refreshes its calendar, which it does while its Calendar view is open.
- `range` is the range read, in this machine's zone.
- `unlinked_accounts` lists accounts of another source that no link joins to a Teams account (their events are not merged). `unlinked_fix` has, in the same order, the exact command that links each one, for example `m365crawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>`.
- `notices` says, when the whole range starts after an account's `teams_cache_fresh_at`, when the Teams calendar cache was last refreshed and that newer events are missing until Teams refreshes it: open the Calendar in Teams, then run `m365crawl sync`. A notice (here, in `calendar event` and in `calendar actions`) also says when the result holds Outlook events while the Outlook source is off for this run, so they are archived data that is not being refreshed, with the age of the last good read.
- `unlinked_recaps` lists the meeting recaps that started in the range and belong to no event (an impromptu meeting, or an event the cache dropped), each with its summary, action items and mentions. Each carries `placed_at` and `placed_by` (`meeting_start`, or `recording_start` when the recap has no meeting start).
- A recap's `link_method` says how it reached its event: `ical_uid` (the recap carries the event's id: certain), `time` (no id, but its start and end each match the event's within a minute) or `start_time` (no id, and exactly one event starts within five minutes of the meeting start: weigh it accordingly). When several occurrences of a series carry the same id, a recap belongs to the occurrence whose start is nearest its `meeting_start` within five minutes; a recap that no occurrence matches stays on every occurrence with `series_level: true`.

An agenda item carries `id` and `event_id` (the same short id, stable when another source is linked; `id` is there because every other command's items use it, and `m365crawl calendar event <id>` takes either), `event_key`, `account_id`, `tenant_id` and `user_id` (when a Teams account is the principal), `sources` (`["teams"]`, `["outlook"]` or both), `ical_uid`, `series_key`, `event_type`, `subject`, `start`, `end` (UTC), `start_local` and `end_local`, `all_day`, `start_date`, `end_date`, `time_zone`, `time_zone_iana`, `status` (`confirmed`, `cancelled` or `declined`), `cancelled`, `response`, `show_as`, `is_organizer`, `is_private`, `organizer_name`, `organizer_address`, `is_online_meeting`, `join_url`, `short_join_url`, `dial_in_conference_id`, `dial_in_toll_number`, `meeting_chat_id`, `location`, `rooms`, `rooms_as_of`, `attendee_count`, `has_attachments`, `body_preview`, `has_recap`, `action_item_count`, `recording_count`, `detail_level`, `detail_as_of`, `last_modified`, `removed`, `removed_by`, `unknown_fields`, `filled_fields` and `overridden_fields`.

- **Not known is not false.** Empty strings and false flags are omitted, with one exception that matters: a flag the source stated is printed `true` or `false`, and a flag it did not state has no key and its name is in `unknown_fields`. So a missing `cancelled` means "not known", never "no".
- **Merged twins.** `filled_fields` lists fields taken from another source (`{"field", "from", "as_of"}`). `overridden_fields` lists each field both copies stated with different values and where the merged value came from (`from` is the source whose value stands, or `union` for `attendees` and `rooms`). Teams owns the Teams meeting fields (`join_url`, `short_join_url`, dial-in, `meeting_chat_id`); the Outlook copy fills them only when Teams lacks them. Attendee and room lists are the union of both copies, with the newer copy's response for an entry both hold.
- `detail_level` is `basic` (only the schedule), `full` (attendees, body and rooms from a copy at least as new as the schedule) or `stale` (the detail is older than the schedule; `detail_as_of` says how old).
- `recording_count` counts the recordings, transcripts and call events of the meeting chat that belong to this occurrence.

On an archive from before the calendar tables, the calendar commands return an empty result with `needs_sync` and a hint to run `sync`.

```sh
m365crawl calendar --days 7
m365crawl calendar --from=-7d --to=tomorrow --query planning
m365crawl calendar --from 2023-11-20 --to 2023-11-25 --fields event_id,subject,start,has_recap
```

`--fields` keys: `id`, `event_id`, `event_key`, `account_id`, `tenant_id`, `user_id`, `sources`, `ical_uid`, `series_key`, `event_type`, `subject`, `start`, `end`, `start_local`, `end_local`, `all_day`, `start_date`, `end_date`, `time_zone`, `time_zone_iana`, `status`, `cancelled`, `response`, `show_as`, `is_organizer`, `is_private`, `organizer_name`, `organizer_address`, `is_online_meeting`, `join_url`, `short_join_url`, `dial_in_conference_id`, `dial_in_toll_number`, `meeting_chat_id`, `location`, `rooms`, `rooms_as_of`, `attendee_count`, `has_attachments`, `body_preview`, `has_recap`, `action_item_count`, `recording_count`, `detail_level`, `detail_as_of`, `last_modified`, `removed`, `removed_by`, `unknown_fields`, `filled_fields`, `overridden_fields`. Only in `calendar event`, and refused here: `organizer`, `attendees`, `attendees_as_of`, `response_counts`, `body_text`, `body_html`, `body_type`, `attachments`, `categories`, `reminder_minutes`, `series`, `recaps`, `chat`, `recordings`, `series_recordings`, `series_recordings_total`, `related_mail`, `transcripts`.

### calendar event

One meeting with everything the archive holds about it, ready for meeting prep.

```
m365crawl calendar event <event> [flags]
```

`<event>` is an item's `id` (the same as its `event_id`), an `event_key`, or an unambiguous prefix of either. An id an earlier state printed (before a source was linked, or before a timed key joined its all-day twin) still resolves. An unknown or ambiguous reference is a `usage` error; the ambiguous one lists up to ten candidates. `--fields` keeps the keys named, in that order; a nested key comes with its parent, so `--fields chat` brings `chat.recent_messages`. `--max-text` cuts the body, summaries, action-item text, the chat's recent message text and the related mail previews, and sets `text_truncated`.

Result: the agenda item, plus:

- `organizer`, `attendees`, `attendees_as_of`, `response_counts`, `body_text`, `body_html`, `body_type`, `attachments`, `categories`, `reminder_minutes` and `series` (`key`, `rule`, `master_event_id`, `occurrence_count_known`).
- `recaps`, each with `call_id`, `headline`, `short_summary`, `outline`, `summary_sections`, `action_items`, `mentions`, `speakers`, `topics`, `recording`, `meeting_start`, `meeting_end`, `expires_at`, `link_method`, `attendance_status` and `attendees_count`.
- `chat`: the Teams meeting chat (`conversation_id`, `display_name`, `message_count`) and `recent_messages`, its newest 20 live messages, newest first, each in the `messages` item shape. `chat` is null when the archive holds no meeting chat for the event. An event with no Teams meeting chat at all, such as one only Outlook holds, also gets a `notices` entry saying so.
- `related_mail`: the Outlook mail about this occurrence, at most 20, always a list (empty when there is none). Gone messages are left out.
  - First, invites (`match: "invite"`): mail whose `ical_uid` is the event's iCal UID, ignoring case.
  - Then, newest first (`match: "subject"`): mail with the same subject once reply and forward prefixes are stripped (`re:`, `fw:`, `fwd:`, `aw:`, `wg:`, `sv:`, ignoring case), received from 14 days before to 7 days after the occurrence's start. When the event names any attendee or organizer address, the mail must also be sent or received by at least one of them. Your own addresses (those signed in to an Outlook profile) do not count toward that, because you are in nearly every meeting; they count only when you are the event's only participant.
  - Each carries `id` (as `mail show` takes it), `match`, `account`, `folder`, `folder_kind`, `subject`, `from_name`, `from_address`, `received_at`, `is_read`, `has_attachments`, `preview` and `ical_uid`.
  - A very common subject ("Standup") shared with the same people can still pull in another meeting's mail from inside the window.
- `recordings`, each with `message_id`, `sent_at`, `kind`, `text`, `link` and `matched_by` (`recap` or `window`); `series_recordings` (recordings of the chat that match no occurrence, newest 20) and `series_recordings_total`.
- `transcripts`: the occurrence's recorded calls, newest first, the same calls `m365crawl transcripts <event_id>` lists, each `{"call_id", "source": "archive", "state", "parts_total", "parts_fetchable", "parts_fetched", "last_fetched_at"}`; `parts_total` leaves out the placeholder of a call that has only a transcript notice. Absent when the occurrence has no recorded call. When a part that can be fetched is not, a `notices` entry says `N of M transcript parts are not fetched; run m365crawl transcripts fetch <event-id>`. Nothing is fetched to answer.
- The archive meta (`archive_age_seconds` and the rest).

In text mode the event ends with a table of its recorded calls and their transcripts, the chat's recent messages and a related-mail table with a `match` column.

```sh
m365crawl calendar event ev_2da7280856 --max-text 400
m365crawl calendar event ev_2da7280856 --fields subject,start,attendees,chat,related_mail
```

`--fields` keys: `id`, `event_id`, `event_key`, `account_id`, `tenant_id`, `user_id`, `sources`, `ical_uid`, `series_key`, `event_type`, `subject`, `start`, `end`, `start_local`, `end_local`, `all_day`, `start_date`, `end_date`, `time_zone`, `time_zone_iana`, `status`, `cancelled`, `response`, `show_as`, `is_organizer`, `is_private`, `organizer_name`, `organizer_address`, `is_online_meeting`, `join_url`, `short_join_url`, `dial_in_conference_id`, `dial_in_toll_number`, `meeting_chat_id`, `location`, `rooms`, `rooms_as_of`, `attendee_count`, `has_attachments`, `body_preview`, `has_recap`, `action_item_count`, `recording_count`, `detail_level`, `detail_as_of`, `last_modified`, `removed`, `removed_by`, `unknown_fields`, `filled_fields`, `overridden_fields`, `organizer`, `attendees`, `attendees_as_of`, `response_counts`, `body_text`, `body_html`, `body_type`, `attachments`, `categories`, `reminder_minutes`, `series`, `recaps`, `chat`, `recordings`, `series_recordings`, `series_recordings_total`, `related_mail`, `transcripts`, `text_truncated`.

### calendar actions

The action items of the recaps held by the events that start in a range, with their owners.

```
m365crawl calendar actions [flags]
```

| Flag | Meaning |
| --- | --- |
| `--from`, `--to`, `--days` | As for the agenda; the range applies to the event's start. Default: today. |
| `--owner=NAME` | Only items whose owner contains NAME, ignoring case. |
| `--mine` | Only your items (see `mine_basis` below). |
| `--limit=50` | Maximum items to return. |

Result: a list sorted by event start, then recap, then the order of the items in the recap. Each item carries `event_id`, `event_key`, `subject`, `event_start`, `call_id`, `title`, `text`, `owner`, `speaker`, `at`, `origin` (`recap` or `catchup`), `mine`, `mine_basis`, `expires_at` and, when true, `series_level`.

Recap owners are mostly a first name alone, so `mine` is decided like this:

- `mine_basis: full_name` when the owner equals your display name (ignoring case).
- `first_name` when the owner is one word equal to your first name and no other attendee of the event has that first word.
- `ambiguous` when someone else in the meeting shares your first name. Then `mine` is absent and `unknown_fields` is `["mine"]`.
- Any other owner has `mine: false` and no basis.

`--mine` keeps `full_name` and `first_name` items, reports how many ambiguous ones it left out as `mine_ambiguous_omitted`, and names the accounts whose own name is not archived in `mine_unknown_accounts`. `--mine` on an account whose own name is not archived yet is a `usage` error; run `sync`, or use `--owner`. The list also carries the agenda's coverage keys, so an empty list on an uncovered day is not read as "no action items". Recaps that no event holds are in the agenda's `unlinked_recaps`, not here.

```sh
m365crawl calendar actions --from yesterday --to today --mine
m365crawl calendar actions --days 7 --owner pat
```

`--fields` keys: `event_id`, `event_key`, `subject`, `event_start`, `call_id`, `title`, `text`, `owner`, `speaker`, `at`, `origin`, `mine`, `mine_basis`, `unknown_fields`, `expires_at`, `series_level`.

### calendar sources

What the archive holds per account and source, and how fresh it is: one row per account and source, Teams first. `--account` keeps that Teams account and the Outlook profiles linked to it.

```
m365crawl calendar sources [flags]
```

- **Every row** has `principal` and `link` (`config`, `address` or `none`; a Teams row is always `none`).
- **A Teams row** has `covered_days` (days the cache ever showed, cumulative), `last_verified_at`, `window_start`, `window_end`, `synced_at`, `cache_fresh_at`, `events_live`, `events_removed`, `events_with_detail`, `events_with_attendees`, `events_with_body`, `events_online`, `recaps_total`, `recaps_with_content`, `recaps_linked`, `recap_action_items` and `unknown_time_zones`. The window is a span, not a coverage claim: `covered_days` is.
- **An Outlook row** adds `status` (`ok`, `skipped_interval`, `unsupported_version`, `unsupported_layout`, `unreadable`), `error` (`code`, `message`, `fix`) when the last read failed, `last_read_at`, `last_attempt_at`, `last_checked_at`, `census_as_of`, `next_read_after` and `read_interval_seconds` (the Outlook store is copied at most once per interval), `unknown_layouts`, `blocks_invalid_ratio`, `unmapped_values` and `deletions`. A status other than `ok` means Outlook is not being read, and `error` says why; a `skipped_interval` row is healthy.

```sh
m365crawl calendar sources
```

`--fields` keys: `source`, `account_id`, `principal`, `link`, `tenant_id`, `user_id`, `window_start`, `window_end`, `covered_days`, `last_verified_at`, `synced_at`, `cache_fresh_at`, `events_live`, `events_removed`, `events_with_detail`, `events_with_attendees`, `events_with_body`, `events_online`, `recaps_total`, `recaps_with_content`, `recaps_linked`, `recap_action_items`, `unknown_time_zones`, `status`, `last_read_at`, `last_attempt_at`, `last_checked_at`, `census_as_of`, `next_read_after`, `read_interval_seconds`, `unknown_layouts`, `blocks_invalid_ratio`, `unmapped_values`, `deletions`, `error`.

## transcripts

Read meeting transcripts offline. A recorded meeting is one call. A call has one part for each stretch that was recorded or transcribed, and each part's transcript is a file of its own in SharePoint. A sync lists the parts from the recording notices it already archived from the meeting chat; nothing is fetched, and `transcripts` and `transcripts show` read only the archive (no network, no browser).

`<meeting>` is an event id or key (as `calendar` prints them), a meeting chat link or thread id, or a call id. A chat or an event can hold several recorded calls. One that names no recorded call is the `unknown_meeting` error (exit 2).

- **A part's `state`** is `ok` when its text is in the archive, `not_fetched` when it can be fetched and has not been, `unfetchable` when no file reference is cached for it, and otherwise the outcome of the last attempt (`no_access`, `not_found`, `no_transcript`, `too_large`, `failed`). `reason` says in one sentence why a part has no text; a part not fetched yet says `not fetched yet; run m365crawl transcripts fetch <call-id>`.
- **A meeting's `state`** is `fetched` (every part that can be fetched has text), `partial`, `not_fetched` or `unfetchable` (no part can be fetched by ids).
- **Where it came from.** Every result has `"source": "archive"`, and `fetched_at` says when each part's text was fetched. Text output ends with `source: archive`.

```
m365crawl transcripts [<meeting>] [flags]
m365crawl transcripts list [<meeting>] [flags]
m365crawl transcripts show <meeting>
```

Without `<meeting>` every recorded meeting is listed, newest first, with its part counts. With `<meeting>` each call of it is listed with its `parts`, in order. `transcripts` and `transcripts list` are the same command.

| Flag | Meaning |
| --- | --- |
| `--since=DATE` | Only meetings that started at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d). |
| `--until=DATE` | Only meetings that started before this time; a date alone (YYYY-MM-DD) includes that whole day. |
| `--state=STATE` | Only meetings in this state: fetched, partial, not_fetched or unfetchable. |
| `--limit=N` | Maximum meetings to return (at most 1000); `truncated` says whether more exist. |

Result: `{"items", "count", "truncated", "source": "archive", "notices"?, "note"?, "archive_age_seconds"}`. Item keys: `call_id`, `thread_id`, `event_key`, `title`, `started_at`, `state`, `parts_total`, `parts_fetchable`, `parts_fetched` and, with `<meeting>`, `parts` (`ordinal`, `part_key`, `starts_at`, `duration_seconds`, `transcribe_only`, `content_types`, `storage_kind`, `ref_quality`, `fetchable`, `state`, `reason`, `fetch`). `fetch` is `{"state", "fetched_at", "attempted_at", "entries", "http_status", "browser"}` or null. A notice says how many listed meetings have parts not fetched yet.

```sh
m365crawl transcripts --since 7d
m365crawl transcripts ev_1234abcd
```

`--fields` keys: `call_id`, `thread_id`, `event_key`, `title`, `started_at`, `state`, `parts_total`, `parts_fetchable`, `parts_fetched`, `parts`.

### transcripts show

Print a meeting's transcript from the archive: every part in time order, with each seam marked and where each part came from.

```
m365crawl transcripts show <meeting>
```

Result: `{"call_id", "event_key", "title", "source": "archive", "complete", "segments": [{"ordinal", "transcribe_only", "starts_at", "fetched_at", "state", "reason", "entries": [{"speaker", "start", "end", "offset", "text"}]}], "text_truncated"?}`. A part with no text keeps its place as a segment with no entries and its `reason`, and `complete` is false. `start` and `end` are absolute times; `offset` (h:mm:ss) is from the start of the part. When `<meeting>` names several recorded calls the newest is shown and a notice says how to pick another. `--max-text` cuts each entry's text and sets `text_truncated`.

In text mode each seam is one line, such as `── part 2 of 3 · 10:02 · fetched 2026-11-09 08:00 ──`, `── part 3 of 3 · 10:40 · not fetched yet; run m365crawl transcripts fetch <call-id> ──` or, after a failed attempt, `── part 2 of 3 · 10:02 · not fetched: <reason> ──`; consecutive lines of one speaker are joined, and the last line says how many parts are fetched.

`--fields` keys: `call_id`, `event_key`, `title`, `source`, `complete`, `segments`, `text_truncated`.

### transcripts fetch

Fetch the transcripts of a meeting's parts from SharePoint and store them, so later reads are offline. Runs an invisible Edge (or Chrome) with m365crawl's own browser profile; m365crawl never sees a password or token.

```
m365crawl transcripts fetch [<meeting>] [flags]
m365crawl transcripts fetch --since=DATE [flags]
```

Pass exactly one of `<meeting>` or `--since`. Parts whose text is already in the archive are not fetched again (state `local`, with their `fetched_at`) unless `--refetch`; parts that cannot be fetched are listed with their `reason` and never sent, including a part whose cached host is not a SharePoint host. With nothing left to fetch no browser starts. This and `transcripts signin` are the only commands that use the network, and only inside the browser: it runs headless with its own profile next to the archive, opens each SharePoint site once and fetches each part inside the page, so m365crawl never handles a password, token or download link. The browser is closed, with every process it started, however the command ends.

| Flag | Meaning |
| --- | --- |
| `--since=DATE` | Instead of `<meeting>`: every recorded meeting that started at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d) and has parts not fetched yet. |
| `--limit=N` | Maximum meetings to fetch (default 20, at most 200); `truncated` says whether more were left. |
| `--refetch` | Also fetch parts whose text is already in the archive. |
| `--browser=edge\|chrome\|PATH` | The browser to run: `edge`, `chrome` or the path of an Edge or Chrome executable. Default: Edge, then Chrome. Env `M365CRAWL_BROWSER`. |
| `--timeout=DURATION` | Stop the whole fetch after this long (default 10m). |

Result: `{"source", "fetched_at", "browser", "calls": [{"call_id", "parts": [{"ordinal", "state", "http_status", "entries", "fetched_at", "reason"}]}], "fetched", "local", "failed", "unfetchable", "truncated", "next", "note"?, "archive_age_seconds"}`. `source` is `network` when a browser ran (`fetched_at` and `browser` say when and which) and `archive` when nothing needed fetching. A part's `state` is `local`, `unfetchable` or this run's outcome (`ok`, `no_access`, `not_found`, `no_transcript`, `too_large`, `failed`). A part SharePoint refuses is stored with its state and the others still fetch; the command exits 0 with a `note`. Text output prints one row per part, the totals, `next` and `source: network · fetched <time>`. Progress lines go to stderr (`m365crawl: ...` in text mode, `{"progress": "..."}` otherwise) and carry counts and states only.

A profile that is not signed in stops the run with `transcripts_signin_required` (exit 3); the parts not fetched stay `not_fetched`. Its fix: ask the user before running `m365crawl transcripts signin`, because it opens a visible window, and run it only after they say yes. If the window asks to enroll or register this device, the organization signs in only from managed devices: tell the user, do not run it again, and keep using the commands that read the archive offline. Other errors: `transcripts_no_browser` (exit 3), `transcripts_browser_busy` (exit 4: a sign-in window is open or another fetch runs), `transcripts_browser_failed` (exit 1, also when `--timeout` ends the run).

```sh
m365crawl transcripts fetch ev_1234abcd
m365crawl transcripts fetch --since 7d --limit 50
```

### transcripts signin

Open m365crawl's browser profile in a visible window once, so you can sign in to SharePoint; `transcripts fetch` then signs in silently.

```
m365crawl transcripts signin [flags]
```

Ask the user before running it: it opens a visible Edge window where they sign in to Microsoft 365 once. Run it only after they say yes. If the window asks to enroll or register this device, the organization signs in only from managed devices: tell the user, do not run it again, and keep using the commands that read the archive offline. When stdin is not a terminal (an agent runs it) it refuses without `--user-agreed` (`signin_needs_agreement`, exit 2); on a terminal it says what will open and waits for Enter. It prints each step on stderr (opening the window, waiting for sign-in, signed in) and waits up to 5 minutes until the window is signed in: on the SharePoint host, past its sign-in pages, and with the site's API answering as for a signed-in user. Then it closes the browser. The window uses m365crawl's own profile next to the archive, never the everyday browser profile.

| Flag | Meaning |
| --- | --- |
| `--host=HOST` | The SharePoint host to sign in to, such as `<tenant>.sharepoint.com` (only SharePoint's own domains). Default: the host that holds most of the archive's transcript parts; with none, a `usage` error names `--host`. |
| `--browser=edge\|chrome\|PATH` | As for `transcripts fetch`. |
| `--user-agreed` | The user has agreed to the visible window. Required when stdin is not a terminal. |

Result: `{"signed_in": true, "browser", "next"}`, or in text mode `signed in; run m365crawl transcripts fetch <meeting>`. A window closed before sign-in finished, or no sign-in within 5 minutes, is `transcripts_browser_failed`.

## metadata

Print the crawlkit app manifest, for `crawlctl discover`.

```
m365crawl metadata [flags]
```

Result: one crawlkit `control.Manifest` JSON document on stdout, indented in every output mode: the app id, description, default database path, the commands crawlkit can run (`overview`, `status`, `sync`, `doctor`, `search`, `calendar`, and `mail list`, `mail show`, `mail thread`, `mail folders` and `mail unread`, `transcripts` and `transcripts show`), capabilities (`doctor`, `status`, `sync`, `watch`, `search`, `sql`, `chats`, `mail`, `calendar`, `transcripts`) and privacy flags. Needs no archive and never syncs.

## skill

Print the agent guide for this version, as Markdown in every output mode.

```
m365crawl skill
```

Result: raw Markdown on stdout, the same text as `.agents/skills/m365crawl/SKILL.md`, embedded in the binary. It is short on purpose: what m365crawl is, privacy, "run `m365crawl` and `m365crawl --help` first", and one table of jobs (triage, recall, threads, cross-source and meeting prep, attachments) with their commands. Flag detail lives in `--help` and this page. Needs no archive.

## version

Print the m365crawl version, commit and build date.

```
m365crawl version
```

Result: `{"version", "commit", "date"}` as one JSON document, or one human line in text mode. `m365crawl --version` prints the same.
