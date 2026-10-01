# Command reference

This reference reproduces the output of `teamscrawl --help` and `teamscrawl <command> --help` for v0.1.0: every flag and help text below is what the binary prints. The normative behavior is in [SPEC.md](../SPEC.md). Run any command with `--help` for the same text.

## Global flags

Every command accepts these. `--fields` and `--max-text` apply to the list commands only (`search`, `messages`, `conversations`, `people`, `activity`, `unread`, `thread`, `watch`); on any other command they are a `usage` error.

| Flag | Meaning |
| --- | --- |
| `--format=text\|json\|log` | Output format: text, json or log. Default: text on a terminal, json otherwise. |
| `--json` | Alias for --format json. |
| `--db=PATH` | Archive database path (default ~/.teamscrawl/teamscrawl.db) ($TEAMSCRAWL_DB). |
| `--teams-root=DIR` | Teams EBWebView directory (default: the new Teams container) ($TEAMSCRAWL_TEAMS_ROOT). |
| `--account=TENANT/USER` | Only this account, as &lt;tenantId&gt;/&lt;userId&gt;. Default: every account. |
| `--no-color` | Disable colored output (also: NO_COLOR). CLICOLOR_FORCE=1 forces color. |
| `--max-age=DURATION` | Read commands sync first when the last successful sync is older than this (for example 15m, 2h, 1d). 0 disables the implicit sync ($TEAMSCRAWL_MAX_AGE). |
| `--fields=a,b,c` | List commands only: keep only these top-level keys of each item, comma separated. |
| `--max-text=N` | List commands only: truncate each item's text to N characters and set text_truncated. 0 keeps all of it. |
| `--version` | Print the version, commit and build date, then exit. |

Output is JSON when stdout is not a terminal and text on a terminal. Errors go to stderr as `{"error": {"code", "message", "fix"}}`. Exit codes: 0 success, 1 runtime failure, 2 usage, 3 environment not ready, 4 another run holds the lock. See [SPEC.md](../SPEC.md) section 6 for every error code.

## Commands

| Command | What it does |
| --- | --- |
| [`doctor`](#doctor) | Check that Teams, Full Disk Access and the archive are ready. |
| [`sync`](#sync) | Copy the Teams cache into the archive once and print what changed. |
| [`status`](#status) | Show archive counts per account, the last sync and other Teams origins. |
| [`search`](#search) | Full-text search over message text, sorted newest first; default --limit 50 (check `truncated`). |
| [`messages`](#messages) | List messages in chronological order (oldest first; with --limit, the newest matches); default --limit 50 (check `truncated`). |
| [`conversations`](#conversations) | List conversations, sorted by last activity, newest first; default --limit 50 (check `truncated`). |
| [`people`](#people) | List people seen as senders or members. |
| [`activity`](#activity) | List activity-feed items (mentions, replies, reactions) with their messages. |
| [`unread`](#unread) | List unread messages (chats and meetings unless --include-channels), newest first; --by-conversation gives per-conversation counts. |
| [`thread`](#thread) | Show one thread: &lt;conversation&gt; &lt;root-message-id&gt;, or a Teams message link. |
| [`watch`](#watch) | Stream one JSON line per new, edited or deleted message or activity item as Teams writes its cache (runs until interrupted). |
| [`whoami`](#whoami) | Show the accounts in the archive and the archive's state. |
| [`sql`](#sql) | Run a read-only SQL query against the archive. |
| [`skill`](#skill) | Print the agent guide (SKILL.md) for this version, as Markdown in every output mode. |
| [`version`](#version) | Print the teamscrawl version, commit and build date. |

## doctor

Check that Teams, Full Disk Access and the archive are ready.

```
teamscrawl doctor [flags]
```

No flags beyond the global ones.

Result: `{"ok", "checks": [{"name", "ok", "warn"?, "detail", "fix"}]}`. Exit 3 (`doctor_failed`) when a required check fails; warnings do not fail it. Does not run an implicit sync. Checks include `archive_newer` (fails), `archive_upgrade` and `last_sync_status` (warnings).

Examples:

```sh
teamscrawl doctor
```

## sync

Copy the Teams cache into the archive once and print what changed.

```
teamscrawl sync [flags]
```

No flags beyond the global ones.

Result: The sync report (see SPEC.md sections 4 and 5). Exit 0 for `ok`, `ok_with_omissions` and `unchanged`. Status `partial` (some sources committed, others failed) prints the report on stdout, a `partial_sync` error on stderr and exits 1. `--account` limits the run to one account and skips the unchanged shortcut.

Examples:

```sh
teamscrawl sync
teamscrawl sync --json
```

## status

Show archive counts per account, the last sync and other Teams origins.

```
teamscrawl status [flags]
```

No flags beyond the global ones.

Result: Archive path, schema version, per-account counts, the last run and other Teams origins seen.

Examples:

```sh
teamscrawl status
```

## search

Full-text search over message text, sorted newest first; default --limit 50 (check `truncated`).

```
teamscrawl search [<query>] [flags]
```

Arguments:

| Argument | Meaning |
| --- | --- |
| `[&lt;query&gt;]` | Words to find; "quoted phrases" and a trailing * for prefixes are supported. Optional when a filter (--mentions-me, --from, --conversation, --team, --since, --until) is given: then the filters alone select the messages. |

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
| `--mentions-me` | Only messages that mention you. |
| `--html` | Add each message's HTML body as html. |

Result: A list of message items, newest first.

Examples:

```sh
teamscrawl search planning --limit 1
teamscrawl search --mentions-me --since 7d --max-text 200
```

## messages

List messages in chronological order (oldest first; with --limit, the newest matches); default --limit 50 (check `truncated`).

```
teamscrawl messages [flags]
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
| `--mentions-me` | Only messages that mention you. |
| `--unread` | Only unread messages (chats and meetings unless --include-channels). |
| `--include-channels` | include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`) |
| `--html` | Add each message's HTML body as html. |

Result: A list of message items, oldest first. Channel thread roots carry `reply_count` and `last_reply_at`.

Examples:

```sh
teamscrawl messages -c "Fixture team 1 › General" --since 2023-11-14 --max-text 80
teamscrawl messages --unread --include-channels --limit 5 --fields id,sender_name,text
```

## conversations

List conversations, sorted by last activity, newest first; default --limit 50 (check `truncated`).

```
teamscrawl conversations [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--kind=STRING` | Only this kind, for example Chat, Topic (channel), Space (team) or Meeting. |
| `--query=STRING` | Find conversations by name, best match first: an exact name, then names that start with the query, then names that contain it, then conversations whose title holds all the words; ties go newest first. |
| `--team=STRING` | Only this team's own conversation and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of conversation items, newest activity first (best match first with `--query`).

Examples:

```sh
teamscrawl conversations --kind Space
teamscrawl conversations --query planning
```

## people

List people seen as senders or members.

```
teamscrawl people [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--query=STRING` | Part of a display name, or an exact person id. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |

Result: A list of person items.

Examples:

```sh
teamscrawl people --query alex
```

## activity

List activity-feed items (mentions, replies, reactions) with their messages.

```
teamscrawl activity [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--unread` | Only unread items. |
| `--type=STRING` | Only these activity types, comma separated, matched exactly in any case. Seen in the cache: mention (you were @-mentioned in a channel, as a team or tag), mentionInChat (in a chat, or by @everyone), reply, replyToReply, follow, reaction, reactionInChat, msGraph (system notices such as meeting updates and approvals), teamMembershipChange, threadActivity. Example: --type mention,mentionInChat. |
| `--team=STRING` | Only items in this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--since=STRING` | Only items at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h). |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of activity items joined with their messages.

Examples:

```sh
teamscrawl activity --unread --limit 5 --fields at,type,conversation_display_name,text
teamscrawl activity --type mention,mentionInChat
```

## unread

List unread messages (chats and meetings unless --include-channels), newest first; --by-conversation gives per-conversation counts.

```
teamscrawl unread [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `-c, --conversation=STRING` | Conversation id, or its exact title or display name. |
| `--team=STRING` | Only this team and its channels: the team's exact name (any case for ASCII letters) or its id. An unknown or ambiguous name is a usage error. |
| `--limit=50` | Maximum items to return; truncated says whether more exist. |
| `--html` | Add each message's HTML body as html. |
| `--include-channels` | include channels (off by default: most channels are never opened, so their unread counts are noise; channel mentions and replies reach you through `activity`) |
| `--by-conversation` | One item per conversation with its unread count, oldest and newest unread time and a link, most unread first (the overview; ignores --html). |
| `--include-system` | Also include Teams' system pseudo-conversations (48:notifications, 48:calllogs, 48:annotations), which mirror real messages and are left out by default. |

Result: A list of message items, newest first; with `--by-conversation`, one overview item per conversation. Results carry `channels_excluded: true` when channels are left out.

Examples:

```sh
teamscrawl unread --limit 5
teamscrawl unread --by-conversation --include-channels
```

## thread

Show one thread: &lt;conversation&gt; &lt;root-message-id&gt;, or a Teams message link.

```
teamscrawl thread <target> [<root>] [flags]
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
teamscrawl thread 19:topicchannel1@thread.tacv2 1700000045000
teamscrawl thread "https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?parentMessageId=1700000045000"
```

## watch

Stream one JSON line per new, edited or deleted message or activity item as Teams writes its cache (runs until interrupted).

```
teamscrawl watch [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--every=DURATION` | Poll interval: the safety net when file events are missed. Syncs run only when the cache changed. |
| `--emit-initial` | Also emit the first sync's changes (by default that sync is a silent baseline and only later changes are emitted). |

Result: JSON Lines, the one exception to the one-document rule (SPEC.md section 7). Exits 0 on SIGINT or SIGTERM.

Examples:

```sh
teamscrawl watch --every 30s --fields id,type,text --max-text 200
```

## whoami

Show the accounts in the archive and the archive's state.

```
teamscrawl whoami [flags]
```

No flags beyond the global ones.

Result: The accounts in the archive (with `self_id`) and the archive's state.

Examples:

```sh
teamscrawl whoami
```

## sql

Run a read-only SQL query against the archive.

```
teamscrawl sql <query> [flags]
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
teamscrawl sql "select kind, count(*) as n from conversations group by kind"
```

## skill

Print the agent guide (SKILL.md) for this version, as Markdown in every output mode.

```
teamscrawl skill [flags]
```

No flags beyond the global ones.

Result: Raw Markdown on stdout in every output mode (the one exception to the JSON default, like `--help`); the same text as `.agents/skills/teamscrawl/SKILL.md`, embedded in the binary. Needs no archive or Teams cache.

Examples:

```sh
teamscrawl skill
```

## version

Print the teamscrawl version, commit and build date.

```
teamscrawl version [flags]
```

No flags beyond the global ones.

Result: `{"version", "commit", "date"}` as one JSON document, or one human line in text mode. `teamscrawl --version` prints the same.

Examples:

```sh
teamscrawl version
teamscrawl --version
```
