# Command reference

This reference reproduces the output of `m365crawl --help` and `m365crawl <command> --help` for v0.2.0: every flag and help text below is what the binary prints. The normative behavior is in [SPEC.md](../SPEC.md). Run any command with `--help` for the same text.

## Platform defaults

| Host | Default `--teams-root` | Default `--db` |
| --- | --- | --- |
| macOS | `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView` | `~/.m365crawl/m365crawl.db` |
| Windows | `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView` | `%LOCALAPPDATA%\m365crawl\m365crawl.db` |

On Windows the default archive path is private by construction. A custom `--db` path is allowed only when its direct parent directory is already private to the current user and SYSTEM or m365crawl can create that parent itself as a new private directory. If that direct parent already exists and is not private, or the archive file already exists and is not private, open fails with `db_error` before SQLite writes anything.

## Global flags

Every command accepts these. `--fields` and `--max-text` apply to the list commands only (`search`, `messages`, `conversations`, `teams`, `people`, `activity`, `stores`, `records`, `unread`, `thread`, `watch`, `calendar`, `calendar event`, `calendar actions`); on any other command they are a `usage` error.

| Flag | Meaning |
| --- | --- |
| `--format=text\|json\|log` | Output format: text, json or log. Default: text on a terminal, json otherwise. |
| `--json` | Alias for --format json. |
| `--db=PATH` | Archive database path (default `~/.m365crawl/m365crawl.db` on macOS, `%LOCALAPPDATA%\m365crawl\m365crawl.db` on Windows) (`$M365CRAWL_DB`). On Windows the default path is private by construction; a custom path must use a private existing parent, let m365crawl create a new private parent, and any pre-existing archive file must already be private, or open fails with `db_error` before SQLite writes anything. |
| `--teams-root=DIR` | Teams EBWebView directory (default: the new Teams container) ($M365CRAWL_TEAMS_ROOT). |
| `--outlook-root=DIR` | New Outlook for Mac profiles directory, read as a second calendar source ($M365CRAWL_OUTLOOK_ROOT). Default: the new Outlook's own directory, read when it is there, with no flag; `none` turns it off. Off when `--teams-root` is set without it. `M365CRAWL_OUTLOOK=1` is accepted and does nothing. The default Outlook never fails a sync: a missing directory, no Full Disk Access or an unreadable store is reported (status `unavailable`) and the Teams sync carries on. A named directory that does not exist is the error `outlook_root_missing` (exit 3) on `sync` and on every read command except `whoami` and `status` (which stay exit 0; `status` reports it in an `outlook` field) and `version`. |
| `--outlook-account=TENANT/USER\|none` | Link the Outlook profile to this Teams account so their events merge; `none` ends the link and keeps it ended. A profile with an account signed in under a Teams account's own address is linked automatically (method `address`); this flag always wins over that. The link is kept, so the flag is needed only to change it. Needs the Outlook source on ($M365CRAWL_OUTLOOK_ACCOUNT). |
| `--outlook-profile=NAME` | The Outlook profile `--outlook-account` applies to: required when more than one profile is under the Outlook root ($M365CRAWL_OUTLOOK_PROFILE). |
| `--account=TENANT/USER` | Only this account, as &lt;tenantId&gt;/&lt;userId&gt;. Default: every account. |
| `--no-color` | Disable colored output (also: NO_COLOR). CLICOLOR_FORCE=1 forces color. |
| `--max-age=DURATION` | Read commands sync first when the last successful sync is older than this (for example 15m, 2h, 1d). 0 disables the implicit sync ($M365CRAWL_MAX_AGE). When a read does sync first, stderr gets one line before it starts (plain text in text mode, `m365crawl: syncing — archive is 2h14m old (max-age 15m)`; one JSON line in json and log mode, `{"notice":"syncing","reason":"stale","archive_age_seconds":N,"max_age_seconds":N}`) and the result gains `synced: {seconds, status}`. |
| `--fields=a,b,c` | List commands only: keep only these top-level keys of each item, comma separated. |
| `--max-text=N` | List commands only: truncate each item's text to N characters and set text_truncated. 0 keeps all of it. |
| `--version` | Print the version, commit and build date, then exit. |

Output is JSON when stdout is not a terminal and text on a terminal. Errors go to stderr as `{"error": {"code", "message", "fix"}}`. Exit codes: 0 success, 1 runtime failure, 2 usage, 3 environment not ready, 4 another run holds the lock. See [SPEC.md](../SPEC.md) section 6 for every error code.

## Commands

| Command | What it does |
| --- | --- |
| [`doctor`](#doctor) | Check that Teams, platform access prerequisites and the archive are ready. |
| [`sync`](#sync) | Copy the Teams cache into the archive once and print what changed. |
| [`status`](#status) | Show archive counts per account, the last sync and other Teams origins. |
| [`search`](#search) | Full-text search over message text, sorted newest first; default --limit 50 (check `truncated`). |
| [`messages`](#messages) | List messages in chronological order (oldest first; with --limit, the newest matches); default --limit 50 (check `truncated`). |
| [`conversations`](#conversations) | List conversations, sorted by last activity, newest first; default --limit 50 (check `truncated`). |
| [`teams`](#teams) | List teams with their channel count, last activity and unread count; the team_id or display_name is what --team takes. |
| [`people`](#people) | List people seen as senders or members. |
| [`activity`](#activity) | List activity-feed items (mentions, replies, reactions) with their messages. |
| [`calendar`](#calendar) | The agenda for a range (default today), merged across sources, with per-principal coverage; `calendar event` shows one event with everything the archive holds about it; `calendar actions` lists the action items of the recaps held by the events of a range; `calendar sources` says what the archive holds per account and source and how fresh it is. |
| [`stores`](#stores) | List every database and object store archived without a typed table, with record counts; the database name is what records --database takes. |
| [`records`](#records) | List archived records of one database (or a prefix of its name), newest change first; value_json and key_json are parsed JSON; default --limit 50 (check truncated). |
| [`unread`](#unread) | List unread messages (chats and meetings unless --include-channels), newest first; --by-conversation gives per-conversation counts. |
| [`thread`](#thread) | Show one thread: &lt;conversation&gt; &lt;root-message-id&gt;, or a Teams message link. |
| [`watch`](#watch) | Stream one JSON line per new, edited or deleted message or activity item as Teams writes its cache (runs until interrupted). |
| [`whoami`](#whoami) | Show the accounts in the archive and the archive's state. |
| [`sql`](#sql) | Run a read-only SQL query against the archive. |
| [`skill`](#skill) | Print the agent guide (SKILL.md) for this version, as Markdown in every output mode. |
| [`metadata`](#metadata) | Print the crawlkit app manifest (for crawlctl discovery). |
| [`version`](#version) | Print the m365crawl version, commit and build date. |

## doctor

Check that Teams, platform access prerequisites and the archive are ready.

```
m365crawl doctor [flags]
```

No flags beyond the global ones.

Result: `{"ok", "checks": [{"name", "ok", "warn"?, "detail", "fix"}]}`. Exit 3 (`doctor_failed`) when a required check fails; warnings do not fail it. Does not run an implicit sync. Checks include `archive_newer` (fails), `archive_upgrade`, `last_sync_status`, `calendar_cache` and `outlook_store` (warnings; `calendar_cache` fires when an account has no Teams calendar database in the archive or the newest Teams calendar cache time is more than 7 days old; `outlook_store` says the Outlook source is off, or lists each profile with whether its store header is a version this build reads, what the last sync recorded and how it is linked to Teams; with Outlook on by default, no Outlook on the machine, no profile and a normal profile are plain passes, and it warns when a named root has no profile, access is denied, a store version is unknown or the last read failed, with the same fix a sync gives). On Windows the `full_disk_access` check is `ok: true` with detail `not applicable on Windows; Teams cache is under LocalCache, not TCC-protected.`

Examples:

```sh
m365crawl doctor
```

## sync

Copy the Teams cache into the archive once and print what changed.

```
m365crawl sync [flags]
```

| Flag | Meaning |
| --- | --- |
| `--full-read` | Read every record in full, even from a cache that has not changed since the last sync, instead of skipping the records whose bytes are unchanged. The archive comes out the same either way; this is a check, not a repair ($M365CRAWL_FULL_READ). |

Result: The sync report (see SPEC.md sections 4 and 5). It carries `calendar` counts (`events`, `recaps`, `recap_items`, `gone`, `linked`, `linked_by_start`, `unlinked_no_event`, `unlinked_ambiguous`, `refused`) for the calendar tables the sync filled from the calendar and recap records; there is no calendar command yet, so read them with `m365crawl sql "select count(*) from calendar_source_events"`. Exit 0 for `ok`, `ok_with_omissions` and `unchanged`. Status `partial` (some sources committed, others failed) prints the report on stdout, a `partial_sync` error on stderr and exits 1. `--account` limits the run to one account and skips the unchanged shortcut.

`--outlook-account` and `--outlook-profile` link an Outlook profile to a Teams account (SPEC.md section 4.2): the link is applied after the sources, a refused link exits 2 with the report on stdout, and `--account` cannot be combined with it. Nothing in the output identifies which Teams account owns an Outlook profile (an Outlook account is `outlook/<profile name>`, and `whoami` has no email), so the caller must know it or ask. The link is applied after the sources are read, so the report can show the Outlook source as `skipped_interval` (a read under 5 minutes old was reused) and `linked: 0` while the link took effect: confirm with `calendar sources`, whose Outlook row then has `link: "config"` (or `"address"` when the sync linked it by an address signed in to the profile) and the Teams account as `principal`.

Examples:

```sh
m365crawl sync
m365crawl sync --json
m365crawl sync --outlook-root ~/outlook --outlook-profile Main --outlook-account <tenantId>/<userId>
```

## status

Show archive counts per account, the last sync and other Teams origins.

```
m365crawl status [flags]
```

No flags beyond the global ones.

Result: Archive path, schema version, per-account counts, the last run and other Teams origins seen.

Examples:

```sh
m365crawl status
```

## search

Full-text search over message text, sorted newest first; default --limit 50 (check `truncated`).

```
m365crawl search [<query>] [flags]
```

Arguments:

| Argument | Meaning |
| --- | --- |
| `[&lt;query&gt;]` | Words to find; "quoted phrases" and a trailing * for prefixes are supported. Optional when a filter (--mentions-me, --direct-mentions, --from, --conversation, --team, --since, --until) is given: then the filters alone select the messages. |

Flags:

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, or its exact title or display name. |
| `--from=STRING` | Sender: a person id, or a case-insensitive part of the name. |
| `--since=STRING` | Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). |
| `--until=STRING` | Only messages at or before this time (same formats as --since). |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |
| `--include-deleted` | Also search deleted messages. |
| `--mentions-me` | Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see mention_kind). |
| `--direct-mentions` | Only messages that mention you by name (mention_kind person), not channel, team, tag or @everyone broadcasts. |
| `--html` | Add each message's HTML body as html. |

Result: A list of message items, newest first. Items that mention you carry `mention_kind`: `person` (you by name), `channel`, `team`, `tag`, `everyone` or `other`.

Examples:

```sh
m365crawl search planning --limit 1
m365crawl search --mentions-me --since 7d --max-text 200
```

## messages

List messages in chronological order (oldest first; with --limit, the newest matches); default --limit 50 (check `truncated`).

```
m365crawl messages [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, or its exact title or display name. |
| `--from=STRING` | Sender: a person id, or a case-insensitive part of the name. |
| `--since=STRING` | Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). |
| `--until=STRING` | Only messages at or before this time (same formats as --since). |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |
| `--include-deleted` | Also list deleted messages. |
| `--mentions-me` | Only messages that mention you (by name, or through a channel, team, tag or @everyone mention; see mention_kind). |
| `--direct-mentions` | Only messages that mention you by name (mention_kind person), not channel, team, tag or @everyone broadcasts. |
| `--unread` | Only unread messages (chats and meetings unless --include-channels). |
| `--include-channels` | include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`) |
| `--html` | Add each message's HTML body as html. |

Result: A list of message items, oldest first. Channel thread roots carry `reply_count` and `last_reply_at`; items that mention you carry `mention_kind` (`person`, `channel`, `team`, `tag`, `everyone` or `other`).

Examples:

```sh
m365crawl messages -c "Fixture team 1 › General" --since 2023-11-14 --max-text 80
m365crawl messages --unread --include-channels --limit 5 --fields id,sender_name,text
```

## conversations

List conversations, sorted by last activity, newest first; default --limit 50 (check `truncated`).

```
m365crawl conversations [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--kind=STRING` | Only this kind, for example Chat, Topic (channel), Space (team) or Meeting. |
| `--query=STRING` | Find conversations by name, best match first: an exact name, then names that start with the query, then names that contain it, then conversations whose title holds all the words; ties go newest first. |
| `--team=STRING` | Only this team's own conversation and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of conversation items, newest activity first (best match first with `--query`). A Meeting conversation that calendar events name as their chat also carries `calendar_series_key` (the series those events share; absent when they belong to several series or are all single events) and `calendar_event_count` (how many live, non-master occurrences of the account hold this chat; a cancelled one is not counted, a declined one is, because the meeting happened). A chat no event names has neither key. Go from the chat to its events with `m365crawl calendar --query <subject>` or `sql` on `calendar_source_events.series_key`.

Examples:

```sh
m365crawl conversations --kind Space
m365crawl conversations --query planning
```

## teams

List teams with their channel count, last activity and unread count; the team_id or display_name is what --team takes.

```
m365crawl teams [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--account=TENANT/USER` | Global flag: only this account's teams. |
| `--fields=a,b,c` | Global flag: keep only these keys of each item (`tenant_id`, `user_id`, `team_id`, `display_name`, `channel_count`, `last_activity_at`, `unread_count`). |

Result: A list of team items `{tenant_id, user_id, team_id, display_name, channel_count, last_activity_at, unread_count}`, newest activity first. `channel_count` counts the team's channels, `last_activity_at` is the newest message time across the team and its channels, and `unread_count` counts unread messages in its channels (channels count here whatever `--include-channels` says elsewhere). Honors `--account`, `--limit` and `--fields`.

Examples:

```sh
m365crawl teams --fields team_id,display_name,unread_count
m365crawl messages --team "Platform" --since 1d
```

## people

List people seen as senders or members.

```
m365crawl people [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--query=STRING` | Part of a display name, or an exact person id. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |

Result: A list of person items.

Examples:

```sh
m365crawl people --query alex
```

## activity

List activity-feed items (mentions, replies, reactions) with their messages.

```
m365crawl activity [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--unread` | Only unread items. |
| `--type=STRING` | Only these activity types, comma separated, matched exactly in any case. Seen in the cache: mention (you were @-mentioned in a channel, as a team or tag), mentionInChat (in a chat, or by @everyone), reply, replyToReply, follow, reaction, reactionInChat, msGraph (system notices such as meeting updates and approvals), teamMembershipChange, threadActivity. Example: --type mention,mentionInChat. |
| `--team=STRING` | Only items in this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--direct-mentions` | Only mention items that name you (type mention or mentionInChat, subtype person, or a message that mentions you by name), not channel, team, tag or @everyone mentions. |
| `--since=STRING` | Only items at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h). |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of activity items joined with their messages. `actor_id` and `actor_name` say who did it and differ from `sender_*` (the related message's author): the reactor for `reaction` and `reactionInChat` (inferred from the reaction nearest in time, so flagged `actor_inferred: true` and possibly wrong when several people reacted at once); the message's sender for `mention`, `mentionInChat`, `reply`, `replyToReply` and `follow`; omitted for `msGraph`, `teamMembershipChange` and `threadActivity`, and when the message is not archived.

Examples:

```sh
m365crawl activity --unread --limit 5 --fields at,type,conversation_display_name,text
m365crawl activity --type mention,mentionInChat
```

## calendar

Read the calendar offline: the agenda for a range (default today), or one event with everything the archive holds about it. The Teams cache holds the days Teams has loaded, so `coverage_gap` says when part of the range is not covered: an absent event is then not evidence that there was none.

```
m365crawl calendar [flags]
m365crawl calendar event <event> [flags]
m365crawl calendar actions [flags]
m365crawl calendar sources [flags]
```

Flags of the agenda (`m365crawl calendar agenda --help` lists them):

| Flag | Meaning |
| --- | --- |
| `--from=WHEN` | Start of the range: today, yesterday, tomorrow, YYYY-MM-DD (midnight in this machine's zone), RFC3339, or a signed offset from now (+3d, -1d, +2w, +90m). Default: today. An unsigned duration such as 7d is a usage error, because `--since` reads it as "back". Write a negative offset with an equals sign, `--from=-7d`: `--from -7d` is a usage error. |
| `--to=WHEN` | End of the range, exclusive; same forms as --from. Default: the start of the next day. Not with --days. |
| `--days=INT` | Range length in days from --from (instead of --to). |
| `--query=STRING` | Only events whose subject, organizer or location contains this text, ignoring case. |
| `--include-cancelled` | Also list cancelled events. |
| `--include-declined` | Also list events you declined. |
| `--include-masters` | Also list recurring masters, which stand for the whole series. |
| `--include-removed` | Also list events a source saw go (`removed` is true on them, `removed_by` names the sources). |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |

`calendar event <event>` takes an `event_id`, an `event_key`, or an unambiguous prefix of either. An id an earlier state printed (before a source was linked, or before a timed key joined its all-day twin) still resolves. An unknown or ambiguous reference is a `usage` error; the ambiguous one lists up to ten candidates by `event_id`. `--fields` keeps the keys named, in that order; `--max-text` cuts the body, summaries and action-item text and sets `text_truncated`.

Result of the agenda: a list `{"items", "count", "truncated", "total"?, "coverage_gap", "coverage_as_of"?, "uncovered_days"?, "uncovered_days_total"?, "accounts"?, "range": {"from", "to", "zone"}, "unlinked_accounts"?, "unlinked_fix"?, "unlinked_recaps"?, "unlinked_recaps_total"?, "archive_age_seconds", ...}`.

- `coverage_gap` is true when some day of the range is not covered by any cached data. `coverage_as_of` is the oldest time a covered day of the range was verified (so "as of three weeks ago" can be said). `uncovered_days` lists the dates of the range some account does not cover (at most 31; `uncovered_days_total` gives the count when it is longer), and `accounts` lists, for each account (an unlinked Outlook profile is its own), `account_id`, `synced_at` and its own `coverage_as_of`, so an agent can tell which account to distrust. When several accounts are in scope, an entry also says which uncovered days it lacks without repeating the list: `"uncovered_all": true` when it lacks every day of `uncovered_days`, or its own `"uncovered_days": ["2031-03-05", ...]` (with `uncovered_days_total` past 31 days) when it lacks fewer. An entry with neither covers the whole range. Example: `"accounts": [{"account_id": "outlook/Main", "uncovered_days": ["2031-03-05", "2031-03-06"]}, {"account_id": "tenant-1/user-1", "uncovered_all": true}]`.
- `range` is the range read, in this machine's zone.
- `unlinked_accounts` lists accounts of another source that no link joins to a Teams account (their events are not merged). `unlinked_fix` has, in the same order, the exact command that links each one: `m365crawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>`, with the Teams account written out when the agenda holds only one. A profile is linked by the operator (`--outlook-account`) or automatically when its own address is the Teams account's own address; nothing links by overlap.

- `notices` (here, in `calendar event` and in `calendar actions`) says when the result holds an event that Outlook holds while the Outlook source is off for this run, so the Outlook data is archived and not refreshed: `"notices": ["the Outlook source is off for this run (--outlook-root none, or --teams-root without --outlook-root), so the Outlook events in this result are archived data that is not being refreshed; last read outlook/Main 3 days ago. Name --outlook-root DIR (and not none) to read Outlook again."]`. The age is from the account's last good read. Text output prints it as one dim `notice:` line. It is absent when the source is on or no Outlook event is in the result.
- `unlinked_recaps` lists the meeting recaps that started in the range and belong to no event of the archive (an impromptu meeting, or an event the cache dropped), each with its summary, action items and mentions; `unlinked_recaps_total` appears only when the limit cut the list. Each carries `placed_at` and `placed_by`: `meeting_start` when the recap has its own meeting start, `recording_start` when it has none and the recording's start stands in for it (then `meeting_start` is absent, and the text says "meeting start unknown"). A recap with neither time cannot be placed in a range and is not listed.
- A recap's `link_method` says how it reached its event: `ical_uid` (the recap carries the event's id: certain), `time` (no id, but its start and end each match the event's within a minute: very likely) or `start_time` (no id, and exactly one event starts within five minutes of the meeting start: a match by time only, not by id, so weigh it accordingly). Cancelled and declined events are never matched, and `start_time` links are recomputed on every sync, so one disappears when it stops being unique or its event goes. When several occurrences of a recurring series carry the same id (the series id), a recap linked to that id belongs to the one occurrence whose start is nearest its `meeting_start` within five minutes (the `start_time` tolerance; the lower key on a tie), and is listed on no other. The choice is made among every occurrence of the id, removed, cancelled and declined ones too, so removing or cancelling an occurrence never moves its recap to another. A recap that no occurrence matches, or that has no `meeting_start`, cannot be placed: it stays on every occurrence with `series_level: true`, and `meeting_start` is the only hint to which one it belongs to. An id that only one occurrence holds attaches all its recaps to it.

An agenda item carries, in this order: `event_id` (a short id computed from the principal and the key, stable when another source is linked), `event_key`, `account_id` (the principal), `tenant_id` and `user_id` (only when a Teams account is the principal), `sources` (`["teams"]`, plus `"outlook"` when one is linked), `ical_uid`, `series_key`, `event_type`, `subject`, `start`, `end` (UTC), `start_local` and `end_local` (in the event's own zone, when it has one Teams can name), `all_day`, `start_date`, `end_date`, `time_zone`, `time_zone_iana`, `status` (`confirmed`, `cancelled` or `declined`), `cancelled`, `response`, `show_as`, `is_organizer`, `is_private`, `organizer_name`, `organizer_address`, `is_online_meeting`, `join_url`, `short_join_url`, `dial_in_conference_id`, `dial_in_toll_number`, `meeting_chat_id`, `location`, `rooms`, `rooms_as_of` (both only when the source stated a room list; otherwise `rooms` is in `unknown_fields` and `location` carries the text), `attendee_count`, `has_attachments`, `body_preview`, `has_recap`, `action_item_count`, `recording_count`, `detail_level`, `detail_as_of`, `last_modified`, `removed`, `removed_by`, `unknown_fields`, `filled_fields` and `overridden_fields`. Strings that are empty and flags that are false are omitted, with one exception that matters: a flag the source stated is printed `true` or `false`, and a flag it did not state has no key and its name is in `unknown_fields`. So a missing `cancelled` means "not known", never "no". `filled_fields` lists fields taken from another source (`{"field", "from", "as_of"}`), and, when two linked copies both list attendees or rooms, the list the union enlarged (`attendees` or `rooms`, from the copy that added entries). `overridden_fields` lists, for a linked twin, each field both copies stated with different values and where the merged value came from (`{"field", "from", "as_of"}`; `from` is the source whose value stands, or `union` for the merged `attendees` and `rooms`), so a value one copy replaced is as visible as one it filled. The Teams meeting belongs to Teams: a Teams copy that states the meeting links (`join_url`, `short_join_url`, `dial_in`, `meeting_chat_id`) supplies them whole, the Outlook copy fills them only when Teams lacks them, an event a Teams copy says is online stays `is_online_meeting: true` while its join link is kept, and `rooms` is the union of the rooms either copy names even when `location` is one copy's text. A linked event's attendee and room lists are the union of both copies' lists, matched by address (attendees) or name (rooms), with the newer copy's response for an entry both hold, and `attendee_count` follows the merged list.

`detail_level` is `basic` (only the schedule), `full` (attendees, body and rooms from a copy at least as new as the schedule) or `stale` (the detail is older than the schedule, so the attendee list may be out of date; `detail_as_of` says how old). `recording_count` counts the recordings, transcripts and call events of the meeting chat that belong to this occurrence (matched by the recap's recording window, then by the occurrence window plus four hours).

Result of `calendar event`: the item above, plus `organizer`, `attendees`, `attendees_as_of`, `response_counts`, `body_text`, `body_html`, `body_type`, `attachments`, `categories`, `reminder_minutes`, `series` (`key`, `rule`, `master_event_id`, `occurrence_count_known`), `recaps` (each with `call_id`, `headline`, `short_summary`, `outline`, `summary_sections`, `action_items`, `mentions`, `speakers`, `topics`, `recording`, `meeting_start`, `meeting_end`, `expires_at`, `link_method`, `attendance_status`, `attendees_count`), `chat` (`conversation_id`, `display_name`, `message_count`), `recordings` (each with `message_id`, `sent_at`, `kind`, `text`, `link` and `matched_by`: `recap` or `window`), `series_recordings` (recordings of the chat that match no occurrence, newest 20) and `series_recordings_total`, then the archive meta. `text_truncated` appears when `--max-text` cut anything.

### calendar actions

The action items of the recaps held by the events that start in a range, with their owners. Flags: `--from`, `--to` and `--days` (as for the agenda; the range applies to the event's start; default today), `--owner=NAME` (the owner's name contains NAME, ignoring case), `--mine` (the owner is you; see `mine_basis` below; the name is the one `whoami` prints for the account, and with no `--account` each event is judged against its own account's name) and `--limit` (default 50). `--fields` applies.

Result: a list sorted by event start, then recap, then the order of the items in the recap. Each item carries `event_id`, `event_key`, `subject`, `event_start`, `call_id`, `title`, `text`, `owner`, `speaker`, `at`, `origin` (`recap` or `catchup`; the recap's own items win over the catch-up's for the same call), `mine`, `mine_basis`, `expires_at` and, when true, `series_level`. Recap owners are mostly a first name alone, so `mine` is decided like this: `mine_basis: full_name` when the owner equals your display name (ignoring case); `first_name` when the owner is one word equal to your first name and no other attendee of the event has that first word (the recap's speakers are used when the event has no attendee list); `ambiguous` when someone else in the meeting shares your first name, and then `mine` is absent and `unknown_fields` is `["mine"]`, because the item may or may not be yours. Any other owner has `mine: false` and no basis. `--mine` keeps `full_name` and `first_name` items and reports how many ambiguous ones it left out as `mine_ambiguous_omitted` (omitted when zero), and names the accounts whose own name is not archived in `mine_unknown_accounts` (an error only when no account has a name). `mine` needs the owner to be your display name or a unique first name, so an owner written "Last, First" does not match; run without `--mine` and read `mine_basis` to see them. The list also carries the agenda's coverage keys (`coverage_gap`, `coverage_as_of`, `uncovered_days`, `accounts`, `range`), so an empty list on an uncovered day is not read as "no action items". Only action items are listed (mentions are in `calendar event`), items a newer copy of the recap dropped are hidden, and an expired recap is still listed, with its `expires_at`. A recap whose occurrence cannot be told (`series_level: true`, see `link_method` above) is listed once, under the first occurrence of the range that holds it, not once per occurrence. `--mine` on an account whose own name is not archived yet is a `usage` error; run `sync`, or use `--owner`. Recaps that no event holds are not listed here; they are in the agenda's `unlinked_recaps`.

```sh
m365crawl calendar actions --from yesterday --to today --mine
m365crawl calendar actions --days 7 --owner pat
```

`calendar sources` answers "what does the archive hold, and how fresh is it", one row per account and source (Teams first). `--account` keeps that Teams account and the Outlook profiles linked to it. A Teams row: `covered_days` (days the cache ever showed, cumulative), `last_verified_at`, `window_start`, `window_end`, `synced_at`, `cache_fresh_at`, `events_live`, `events_removed`, `events_with_detail`, `events_with_attendees`, `events_with_body`, `events_online`, `recaps_total` (all recap rows), `recaps_with_content` (placeholders create no row; a row with neither text nor a live item is not counted), `recaps_linked` (rows with an iCalUID, content or not), `recap_action_items`, `unknown_time_zones` (zone names with no known IANA id, at most 50). Every row also has `principal` and `link` (`config` or `none`; a Teams row is always `none`, and a link shows on the Outlook rows pointing to its principal). The window is a span, not a coverage claim: `covered_days` is. An Outlook row adds `status` (`ok`, `skipped_interval`, `unsupported_version`, `unsupported_layout`, `unreadable`), `error` (`code`, `message`, `fix`) when the last read failed, `last_read_at` (last good read), `last_attempt_at` (last copy), `last_checked_at` (latest sync that looked at the store, even when it was unchanged and read nothing), `census_as_of` (when the read behind the census fields happened; they stay from the last good read on a failure row), `next_read_after` and `read_interval_seconds` (the Outlook store is copied at most once per interval), `unknown_layouts` (`class`, `tag`, `count`: event or detail objects of a tag this build does not know; other classes are not calendar and are not listed), `blocks_invalid_ratio`, `unmapped_values` and `deletions: "unverified"`. A status other than `ok` means Outlook is not being read, and `error` says why; a `skipped_interval` row is healthy.

On an archive from before the calendar tables, all three commands return an empty result with `needs_sync` and a hint to run `sync`.

Examples:

```sh
m365crawl calendar --days 7
m365crawl calendar --from=-7d --to=tomorrow --query planning
m365crawl calendar --from 2023-11-20 --to 2023-11-25 --fields event_id,subject,start,has_recap
m365crawl calendar event ev_2da7280856 --max-text 400
m365crawl calendar sources --account 00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1
```

## stores

List every database and object store archived without a typed table, with record counts; the database name is what records --database takes.

```
m365crawl stores [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--account=TENANT/USER` | Global flag: only this account's databases. A database whose name carries no account is hidden by it. |
| `--fields=a,b,c` | Global flag: keep only these keys of each item (`database`, `store`, `records`, `removed`, `last_updated_at`). |

Result: A list of store items `{database, store, records, removed, last_updated_at}`, sorted by database and store, never truncated. In text mode `database` is the manager label and a separate `account` column shows the shortened tenant and user ids. `records` counts the live rows and `removed` the rows Teams' cache no longer holds.

Examples:

```sh
m365crawl stores
m365crawl stores --fields database,store,records
```

## records

List archived records of one database (or a prefix of its name), newest change first; value_json and key_json are parsed JSON; default --limit 50 (check truncated).

```
m365crawl records --database=STRING [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--database=STRING` | Database name or a prefix of it, for example Teams:calendar-manager. See the stores command for the names. |
| `--store=STRING` | Only this object store, matched exactly. |
| `--since=STRING` | Only records changed at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h). A removal counts as a change. |
| `--include-removed` | Also include records Teams' cache no longer holds (they keep their last value and have removed_at set). |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--account=TENANT/USER` | Global flag: only this account's databases. A database whose name carries no account is hidden by it. |
| `--fields=a,b,c` | Global flag: keep only these keys of each item (`source`, `tenant_id`, `user_id`, `database`, `store`, `key_json`, `value_json`, `first_seen_at`, `updated_at`, `removed_at`). |
| `--max-text=N` | Global flag: a `value_json` longer than N characters becomes a JSON string of its first characters and `text_truncated` is true. |

Result: A list of record items `{source, tenant_id?, user_id?, database, store, key_json, value_json?, first_seen_at, updated_at, removed_at?, text_truncated?}`. `key_json` and `value_json` are parsed JSON, not strings. `value_json` is omitted when the value never decoded.

Examples:

```sh
m365crawl records --database Teams:calendar-manager --limit 10 --max-text 300
m365crawl records --database Teams:pinned-manager --store pins --since 7d
```

## unread

List unread messages (chats and meetings unless --include-channels), newest first; --by-conversation gives per-conversation counts.

```
m365crawl unread [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, or its exact title or display name. |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--since=STRING` | Count only unread messages sent at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). Use it for "what needs my attention": old read markers leave stale conversations with hundreds of unread messages. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--html` | Add each message's HTML body as html. |
| `--include-channels` | include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`) |
| `--by-conversation` | One item per conversation with its unread count, oldest and newest unread time and a link, most unread first (the overview; ignores --html). |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of message items, newest first; with `--by-conversation`, one overview item per conversation. Results carry `channels_excluded: true` when channels are left out.

Examples:

```sh
m365crawl unread --limit 5
m365crawl unread --by-conversation --include-channels
m365crawl unread --by-conversation --since 7d
```

## thread

Show one thread: &lt;conversation&gt; &lt;root-message-id&gt;, or a Teams message link.

```
m365crawl thread <target> [<root>] [flags]
```

Arguments:

| Argument | Meaning |
| --- | --- |
| `&lt;target&gt;` | Conversation id, or a Teams message link. |
| `[&lt;root&gt;]` | Root message id (not needed with a link). |

Flags:

| Flag | Meaning |
| --- | --- |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-deleted` | Also show deleted messages. |
| `--html` | Add each message's HTML body as html. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of message items, root first.

Examples:

```sh
m365crawl thread 19:topicchannel1@thread.tacv2 1700000045000
m365crawl thread "https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?parentMessageId=1700000045000"
```

## watch

Stream one JSON line per new, edited or deleted message or activity item as Teams writes its cache (runs until interrupted).

```
m365crawl watch [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--every=DURATION` | Poll interval: the safety net when file events are missed. Syncs run only when the cache changed. |
| `--min-interval=DURATION` | Least time between the end of one sync and the start of the next. A busy Teams cache changes constantly, so without a pause watch would sync back to back. Changes that arrive meanwhile are coalesced into one sync. 0 disables the pause ($M365CRAWL_WATCH_MIN_INTERVAL). |
| `--emit-initial` | Also emit the first sync's changes (by default that sync is a silent baseline and only later changes are emitted). |

Result: JSON Lines, the one exception to the one-document rule (SPEC.md section 7). Exits 0 on SIGINT or SIGTERM.

Examples:

```sh
m365crawl watch --every 30s --fields id,type,text --max-text 200
```

## whoami

Show the accounts in the archive and the archive's state.

```
m365crawl whoami [flags]
```

No flags beyond the global ones.

Result: The accounts in the archive (with `self_id`) and the archive's state.

Examples:

```sh
m365crawl whoami
```

## sql

Run a read-only SQL query against the archive.

```
m365crawl sql <query> [flags]
```

Arguments:

| Argument | Meaning |
| --- | --- |
| `&lt;query&gt;` | One SELECT (or WITH/EXPLAIN/VALUES) statement. |

Flags:

| Flag | Meaning |
| --- | --- |
| `--limit=50` | Maximum rows to return; the query stops there and truncated says whether more rows exist. |

Result: `{"columns", "rows", "count", "truncated"}`; it stops reading at `--limit`, so there is no `total`. Returns every account's rows regardless of `--account`.

Examples:

```sh
m365crawl sql "select kind, count(*) as n from conversations group by kind"
```

## skill

Print the agent guide (SKILL.md) for this version, as Markdown in every output mode.

```
m365crawl skill [flags]
```

No flags beyond the global ones.

Result: Raw Markdown on stdout in every output mode (the one exception to the JSON default, like `--help`); the same text as `.agents/skills/m365crawl/SKILL.md`, embedded in the binary. Needs no archive or Teams cache.

Examples:

```sh
m365crawl skill
```

## metadata

Print the crawlkit app manifest (for crawlctl discovery).

```
m365crawl metadata [flags]
```

No flags beyond the global ones.

Result: One crawlkit `control.Manifest` JSON document on stdout, indented in every output mode and regardless of `--format` (like `skill`, it has one fixed format): the app id, description, default database path, the `status`, `sync`, `doctor` and `search` commands crawlkit can run, capabilities and privacy flags. Needs no archive or Teams cache and never runs the implicit sync.

Examples:

```sh
m365crawl metadata --json
```

## version

Print the m365crawl version, commit and build date.

```
m365crawl version [flags]
```

No flags beyond the global ones.

Result: `{"version", "commit", "date"}` as one JSON document, or one human line in text mode. `m365crawl --version` prints the same.

Examples:

```sh
m365crawl version
m365crawl --version
```
