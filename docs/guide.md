# Usage guide

This guide follows the README's quick start: a walkthrough on synthetic data, then Teams, Outlook mail, the calendar and the output contract agents rely on. Notes, document metadata, people observations and cached Loop channels are in [local-sources.md](local-sources.md); explicit Office, SharePoint, Stream and Engage capture is in [content.md](content.md). [commands.md](commands.md) has every command and flag; [SPEC.md](../SPEC.md) is normative.

## Try it on synthetic data

These examples run against synthetic data from the repository, so every name and message is made up: the committed Teams fixture, and a showcase Outlook store with mail and meetings that `scripts/hxshowcase` writes (the same store `make screenshot` uses). From a clone, build that store and point m365crawl at both and at a scratch archive:

```sh
tmp="$(mktemp -d)"
go run ./scripts/hxshowcase -out "$tmp/outlook"
export M365CRAWL_TEAMS_ROOT="$PWD/testdata/teams-fixture/EBWebView"
export M365CRAWL_OUTLOOK_ROOT="$tmp/outlook"
export M365CRAWL_DB="$tmp/m365crawl.db"
```

On Windows, in PowerShell (Teams fixture only):

```powershell
$env:M365CRAWL_TEAMS_ROOT = (Resolve-Path '.\testdata\teams-fixture\EBWebView').Path
$env:M365CRAWL_DB = Join-Path $env:TEMP 'm365crawl-fixture.db'
```

Against your own Microsoft 365, skip those lines. Then:

```sh
m365crawl sync
m365crawl
m365crawl search planning
m365crawl mail unread
m365crawl mail show outlook/Main:3001
m365crawl calendar --days 14
m365crawl calendar event ev_2da7280856
```

`m365crawl` with no arguments prints an overview: what the archive holds per source (chats, mail, calendar, documents, notes, Loop channels, people and transcripts), how fresh each one is, and the commands to start with. Documents, notes and Loop channels that were never registered say which sync command adds them, and failed sources point to their `sources` command. `people --help` explains the recency column, the ID kinds and the `--account` value without needing a guide. Text times carry their zone (`2026-10-08 02:51 PDT`); JSON times are UTC.

Here is what three of those commands print. The output comes from a run against the same synthetic data; the dates follow the day you build the showcase store.

`m365crawl` (the overview):

```
        ▄████▄ ▄████▄ ██████                           ▄▄
            ██ ██     ██                               ██
██▀█▀██   ███▀ █████▄ █████▄ ▄████ ████▄  ▀▀█▄ ██   ██ ██
██ █ ██     ██ ██  ██     ██ ██    ██ ▀▀ ▄█▀██ ██ █ ██ ██
██   ██ ▀████▀ ▀████▀ ▀████▀ ▀████ ██    ▀█▄██  ██▀██  ██
local-first Microsoft 365 mirror for SQLite  |  overview

source       state           holds                                                             last sync
-----------  --------------  ----------------------------------------------------------------  --------------------
chats        ok              120 messages in 14 conversations, newest 2023-12-05 10:31 PST     2026-10-08 02:51 PDT
mail         ok              37 messages from 2026-09-24, newest 2026-10-08 02:39 PDT          2026-10-08 02:51 PDT
calendar     ok              44 events from 2023-11-20 to 2026-10-23; today not fully covered  2026-10-08 02:51 PDT
documents    not_registered  no sources registered                                             -
notes        not_registered  no sources registered                                             -
loop         not_registered  2 archived records; no sources registered                         -
people       ok              10 people (Teams and mail); no native Outlook people sources      2026-10-08 02:51 PDT
transcripts  empty           no recorded meetings                                              -
calendar: today is not fully covered by cached calendar data: run m365crawl sync, or open the calendar in Teams or Outlook
documents: no Office document sources registered: run m365crawl docs sync (Mac Office) or docs sync --root DIR
notes: no OneNote indexes registered: run m365crawl notes sync, or notes sync --root DIR
loop: 2 cached Loop records are archived and none is registered: run m365crawl loop sync
transcripts: no recorded meetings in the archive yet: run m365crawl sync

Start here:
  m365crawl sync              read Teams, Outlook mail and the calendar into the archive
  m365crawl                   what the archive holds and how fresh it is
  m365crawl search "words"    search chats, mail, fetched transcripts, notes, document metadata, Loop text and archived cloud content
  m365crawl calendar          today's meetings; calendar event <id> for one meeting with its chat and mail
  m365crawl mail unread       unread mail by folder
  m365crawl unread            unread Teams chats
  m365crawl mail list --has-attachments   mail with files; mail show <id> lists each file's name, size and type
  m365crawl docs list         Office recent, shared and recommended document metadata and OneDrive usage records; docs sync registers the sources
  m365crawl notes search "words"   OneNote indexed notes; notes sync registers the indexes
  m365crawl loop search "words"   cached Loop channel text (partial snapshots); loop sync registers the archived records
  m365crawl people            Teams people, mail correspondents and native Outlook people observations
```

`m365crawl search planning --limit 4`: chats and mail together, newest first, with the `thread` to open next:

```
at                    source  where                      who           thread                                          text
--------------------  ------  -------------------------  ------------  ----------------------------------------------  ---------------------------------------------
2026-10-07 00:51 PDT  mail    Inbox                      Casey Brooks  outlook/Main:3007                               Q4 planning offsite agenda - Draft agenda ...
2026-10-03 00:51 PDT  mail    Sent Items                 Riley Chen    outlook/Main:3023                               Re: Q4 planning offsite agenda - Added a s...
2023-11-14 14:14 PST  chats   Fixture team 1 › Planning  Pat Example   19:planningchannel1@thread.tacv2 1700000047000  Planning channel message
2023-11-14 14:14 PST  chats   Fixture team 2 › Planning  Pat Example   19:planningchannel2@thread.tacv2 1700000047000  Planning channel message

4 items
archive age: 1m15s
```

`m365crawl mail show outlook/Main:3001 --json --max-text 200`:

```json
{"id":"outlook/Main:3001","account":"outlook/Main","folder":"Inbox","folder_kind":"inbox","to_me":false,"subject":"Launch checklist: final review","from_name":"Sam Ortiz","from_address":"sam.ortiz@example.test","recipients":[{"name":"Riley Chen","address":"riley.chen@example.test","kind_raw":1}],"in_reply_to":null,"received_at":"2026-10-08T09:39:00Z","sent_at":"2026-10-08T09:38:00Z","is_read":false,"read_state":"unread","flag":"none","importance":"high","has_attachments":true,"attachments":[{"name":"launch-checklist.pdf","size":49000,"content_type":"application/pdf","inline":false,"downloaded":true}],"preview":"Everything is green except the docs pass. Can you take a last look before 3?","body_text":"Everything is green except the docs pass. Can you take a last look before 3? Sam Ortiz","body_state":"inline","internet_message_id":"<showcase-001@example.test>","ical_uid":null,"gone_at":null,"evicted_at":null,"synced_at":"2026-10-08T09:51:45.16Z","archive_age_seconds":58}
```

Output is JSON when stdout is not a terminal and readable text on a terminal. Force either with `--format json|text`.

## Teams

m365crawl reads chats, channels, meeting chats, the activity feed, unread state and mentions from the new Teams app's cache. The Teams commands keep their short names: `messages`, `unread`, `thread`, `conversations`, `teams`, `activity` and `people`. `messages`, `thread` and `unread` cover Teams chats and channels only; for Outlook mail use `m365crawl mail …`.

```sh
m365crawl unread --since 7d --by-conversation
m365crawl activity --unread
m365crawl thread 19:topicchannel1@thread.tacv2 1700000045000
m365crawl messages --team "Fixture team 1" --since 2023-11-14 --max-text 80
```

- **Deep links.** Every message and activity item carries a `link`, a Teams deep link you can cite or open. `thread` accepts such a link in place of the conversation and root id.
- **Threads.** In text output, `messages` and `search` have a `thread` column with the two arguments `thread` takes: `<conversation_id> <reply_chain_id, else id>`. For mail, the column holds the id that `mail thread` takes.
- **Mentions.** An @-mention shows in `text` as the person's plain name. `mentions` lists who was mentioned, `mentions_me` is exact and `mention_kind` (`person`, `channel`, `team`, `tag`, `everyone`) says how you were mentioned. `--direct-mentions` keeps only `person` mentions.
- **System pseudo-conversations.** `48:notifications`, `48:calllogs` and `48:annotations` duplicate real messages, so they are hidden by default; `--include-system` brings them back. Bot cards, call events and thread events read as plain text (`Call ended · 23m`), never raw JSON.
- **Unread.** Channels are left out of `unread` by default because their unread counts are noise; `--include-channels` adds them. Use `--since` for "what needs my attention", because old read markers leave stale chats with hundreds of unread messages.
- **App and citation evidence.** Each Teams message item carries `app_observation`: the app, reply and citation claims of its archived native JSON (for example a bot reply with its cited sources), with a state of `mapped`, `partial`, `absent`, `failed` or `not_derived`. The result's `application_evidence_coverage` counts those states for the messages it returned, and its `posted_by_app` counts the ones that name an app; `--fields app_summary` gives the compact form. The claims are the message's own, not a verified app identity, and a message stays one item. The first sync after upgrading derives this evidence offline from the archive.
- **Accounts.** `--account <tenantId>/<userId>` limits any command to one signed-in account; `m365crawl whoami` lists them.

### Watch for changes

`watch` streams changes as Teams writes them. It prints JSON Lines (one object per line, the one exception to the one-document rule): a `message` or `activity` line per change, then a `sync` line per sync. The first sync is a silent baseline, so only later changes appear; `--emit-initial` prints that one too.

```sh
m365crawl watch --fields id,type,text,sender_name --max-text 200
```

Run it in the background and read its stdout; Ctrl-C (or SIGTERM) stops it with exit 0. A change reaches the output after Teams flushes it to its cache plus a few seconds of debounce. Measured against a real cache, that was about 12 to 32 seconds from the moment a message was sent.

## Outlook mail

m365crawl reads messages, folders, read and flag state, recipients, attachments (as metadata) and bodies as text from new Outlook's local cache: HxStore on Mac and OWA IndexedDB on Windows. Explicit supported copied OWA roots work portably. Windows exposes ordered To/Cc/Bcc roles and conversation identities; Mac roles and missing conversation identities are null. Cache coverage is not server history or server totals. See [source differences](outlook-store.md#windows-owa-cached-mail) for unavailable fields, layout refusals and calendar limits.

```sh
m365crawl mail list --folder inbox --unread
m365crawl mail list --from pat --since 7d
m365crawl mail show <id>
m365crawl mail thread <id>
m365crawl mail folders
m365crawl mail unread
```

- **What the cache holds.** Outlook keeps a window of recent mail per folder. Every mail result says when mail was last synced and how far back the cache reaches (`cache covers since …`). Mail you synced earlier stays in the archive when Outlook drops it from the cache and is marked `evicted`. A message deleted in Outlook is marked `gone` once two reads in a row miss it. Both are hidden unless you pass `--include-evicted` or `--include-gone`.
- **Unread counts can be lower than Outlook's**, because the local cache does not hold every message the server knows about. The output says so.
- **Threads.** `mail thread` first groups messages that share a cached conversation identity (Windows keeps one; Mac Outlook keeps no thread id locally), then follows reply links (In-Reply-To and Message-ID), then falls back to the same subject with a shared participant. The result says which it used (`grouping`: `conversation`, `reply_chain` or `subject`).
- **Recipients** carry names and addresses. On Windows they keep their To, Cc and Bcc roles in order; Mac Outlook's local store does not tell To from Cc, so on Mac the role is null.
- **Attachments** are listed with name, size and content type. Their bytes are not copied into the archive.
- **Meeting invites** carry the meeting's iCal UID, which links them to their calendar event.
- **Ids.** A message id is `<account>:<number>`, for example `outlook/Main:12345`, as `mail list` prints it.

## Calendar

`m365crawl calendar` is the agenda for a range (default today), read from two sources: the meetings Teams cached, with their recaps and action items, and the new Outlook for Mac calendar.

```sh
m365crawl calendar --from tomorrow --days 7
m365crawl calendar event <event_id> --max-text 400
m365crawl calendar actions --from=-7d --to=tomorrow --mine
m365crawl calendar sources
```

- **Meeting prep.** `calendar event` shows one meeting with its attendees, body, recaps and action items, recordings, the meeting chat with its 20 newest messages (`chat.recent_messages`), and related mail (`related_mail`). Related mail is the meeting's invites first (matched by iCal UID, `match: "invite"`), then mail with the same subject from 14 days before to 7 days after the meeting, sent or received by one of its attendees (`match: "subject"`).
- **Two sources, one item.** When an Outlook profile is linked to your Teams account, the two copies of a meeting merge into one item with `sources: ["teams","outlook"]`. The copy that is more up to date wins for the schedule; attendees and rooms are the union of both; Teams owns the Teams meeting fields (join link, dial-in, meeting chat).
- **Coverage.** Teams caches the days you looked at, not a range, so every calendar result says whether the range is covered (`coverage_gap`, `uncovered_days`, `coverage_as_of`). An event missing from an uncovered day is not evidence that it does not exist.
- **Not known is not empty.** Teams fetches attendees, body and rooms only for meetings you opened. A field the source never stated has no key, and its name is in `unknown_fields`.
- **Recaps and action items.** `calendar actions` lists the action items of the recaps in a range with their owners; `--mine` keeps yours and says how the owner was matched (`mine_basis`).
- **What the archive holds.** `calendar sources` shows, per account and source, how many days are covered, when each was last read and what could not be read.

**Linking Outlook to Teams.** An Outlook profile joins your Teams account on its own when an address the profile is signed in with is the Teams account's own address (the match ignores case; `calendar sources` shows `link: address`). Otherwise it stays its own account, because two people invited to the same meeting hold the same events and m365crawl never guesses which account is yours. The agenda then lists the unlinked profile with the command that links it, for example `m365crawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>`. The linking `sync` may print `skipped_interval` and `linked: 0` for Outlook; that is not a failure, and `calendar sources` shows `link: config` once the link is in place. `--outlook-account` always wins over the automatic link, and `none` keeps a profile unlinked.

**Turning Outlook off.** The Outlook store is read by default when it is installed. Turn it off with `--outlook-root none` or `M365CRAWL_OUTLOOK_ROOT=none`, or point it at another directory with `--outlook-root DIR`. With `--teams-root` set and no `--outlook-root`, Outlook stays off. Turning Outlook off turns off both mail and the Outlook calendar.

## Output contract for agents

Run `m365crawl` for an overview and `m365crawl --help` for where to start; every command's `--help` is complete. `m365crawl skill` prints a short guide, the same text as [`.agents/skills/m365crawl/SKILL.md`](../.agents/skills/m365crawl/SKILL.md).

- Results go to stdout, progress and warnings to stderr. In JSON mode each command prints exactly one document.
- Lists are `{"items": [...], "count": N, "truncated": bool}` with `--limit` (default 50). A truncated list also has `"total": N` where the exact count is known. An empty list carries a `note` that says why: no archive yet, no data of that source yet, a time range outside what the archive holds, filters that matched nothing, a source this system does not read, or an account the archive does not hold.
- Errors are `{"error": {"code", "message", "fix"}}` on stderr, and `fix` is an instruction you can follow. Exit codes: 0 success, 1 runtime failure, 2 usage, 3 environment not ready, 4 another run holds the lock.
- Every item carries an id or link for citing, and every read result carries `archive_age_seconds`.
- Nothing ever writes to Teams or Outlook.

Flags that matter for agents:

- `--max-age 15m` (or `M365CRAWL_MAX_AGE`) makes a read command sync first when the last successful sync is older. `0` disables it. When a read syncs first it prints one line on stderr beforehand and the result gains `synced: {seconds, status}`. If that sync fails, the command still answers from the archive, warns on stderr and adds a `sync_error` field.
- `--fields a,b,c` keeps only those top-level keys of each item.
- `--max-text N` truncates each item's text to N characters, including the trailing `…`, and sets `text_truncated`.
- An archive with no complete sync yet adds `"needs_sync": true` and `"hint": "run m365crawl sync"` to every read result. Check for it before you read an empty result as "no match".

Every command takes the global flags `--format`, `--json`, `--db`, `--teams-root`, `--outlook-root`, `--outlook-account`, `--outlook-profile`, `--account`, `--no-color`, `--max-age`, `--fields` and `--max-text`. Text output is colored on a terminal. `--no-color` or `NO_COLOR` turns color off, and `CLICOLOR_FORCE=1` turns it on when output is piped. JSON output is never colored.
