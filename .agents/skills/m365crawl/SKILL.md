---
name: m365crawl
description: Use when an agent needs to read the user's Microsoft Teams chats and channels, Outlook mail or calendar on this Mac or Windows PC, offline, through the local `m365crawl` CLI, which mirrors them into one SQLite archive.
---

# m365crawl

m365crawl mirrors Microsoft 365 on this machine (Teams chats and channels, the new Outlook for Mac's mail, and the Teams and Outlook calendar) into one local SQLite archive, and answers from it offline and read-only. It cannot send, reply, react or mark anything read. It knows only what the desktop apps cached, so old history can be missing. `m365crawl skill` prints this guide for the installed version; SPEC.md in https://github.com/ourostack/m365crawl is the full contract.

## Privacy

- The archive holds the user's private chats and mail. Quote from it only where the user asked; never paste it into issues, logs or other tools unasked.
- Sign-in credentials are never read, and token-shaped values are stored as `[redacted]`.
- Never edit Teams' or Outlook's folders, or the archive, by hand; use the commands.
- `transcripts fetch` and `transcripts signin` are the only commands that use the network, through Edge (or Chrome) with m365crawl's own browser profile; every other command reads the archive offline. When `transcripts fetch` fails with `transcripts_signin_required`, ask the user before running `m365crawl transcripts signin`: it opens a visible Edge window where they sign in to Microsoft 365 once. Run it only after they say yes, then pass `--user-agreed` and relay the steps it prints.

## Start here

Run `m365crawl` first: with no command it says what the archive holds per source (chats, mail, calendar), how fresh each is and what to run next, and it never syncs. Then read `m365crawl --help` and the `--help` of each command you use: they list every flag, sort order and field, so this guide does not repeat them.

- Output is JSON when stdout is not a terminal; pass `--json` to be sure. An error is one JSON line on stderr with a `fix`: follow it.
- Read commands sync first when the archive is older than `--max-age` (default 15m); `--max-age 0` reads it as it is.
- Lists stop at `--limit`, so check `truncated`. An empty list carries a `note` saying why: no archive, filters that matched nothing, or a range outside what the apps cached. `needs_sync` means no data yet, not no match.
- Unknown is not none. `coverage_gap` on a calendar result means some days are not cached, so a missing meeting is not evidence; a field named in `unknown_fields` was never stated.
- `unread` and `messages --unread` cover chats and meetings; channels are left out (`channels_excluded`) because most are never opened. Add `--include-channels` to count them; channel mentions and replies reach you through `activity`.
- `messages` and `search` text output has a `thread` column that names the whole thread: for a chat message the two arguments of `m365crawl thread <conversation_id> <root_id>`, where the root is the message's `reply_chain_id`, else `id` (a channel reply's own id reads only that reply); for mail the id for `m365crawl mail thread <id>`.
- `--fields` and `--max-text` keep results small, for example `m365crawl search "budget" --fields source,id,sent_at,subject,text --max-text 300`.

## Jobs

| Job | Commands |
| --- | --- |
| Read a chat or channel | `m365crawl conversations --query <name>` to find it, then `m365crawl messages --conversation <id> --since 7d` |
| What someone said in a chat yesterday | `m365crawl messages --conversation <id> --from <name> --since <date>` (yesterday as YYYY-MM-DD), or `m365crawl search --from <name> --since 1d` across every chat |
| Mentions of me | `m365crawl search --mentions-me --since 7d` (`--direct-mentions` leaves out @channel and @team), `m365crawl activity --unread` |
| Triage: what needs me | `m365crawl unread --by-conversation --since 7d`, `m365crawl mail unread`, `m365crawl activity --unread` |
| Recall: what was said about something | `m365crawl search "words" --since 30d`, `m365crawl mail list --from <name>`, `m365crawl mail folders` (how far back mail reaches) |
| Threads: the whole conversation | `m365crawl thread <conversation_id> <root_id>` (the root is `reply_chain_id`, else `id`), `m365crawl mail thread <id>` |
| Cross-source and meeting prep | `m365crawl search "words"` (chats and mail), `m365crawl calendar`, then `m365crawl calendar event <event_id>`: one meeting with its `chat` (its newest messages in `chat.recent_messages`, kept by `--fields chat`) and `related_mail`, matched by invite or by subject from 14 days before to 7 days after the start; `m365crawl people --query <name>` finds Teams people and mail correspondents (`id` `mail:<address>`) |
| Attachments | `m365crawl mail list --has-attachments`, `m365crawl mail show <id>` (each file's name, size and type) |

## Hosts

- macOS: `brew install ourostack/tap/m365crawl` or the release tarball. The app that runs m365crawl needs Full Disk Access; `m365crawl doctor` names it.
- Windows: the release zip. Its binaries are intentionally unsigned. Teams chats and the calendar work; mail is read on macOS only for now, so the mail commands fail with `mail_unsupported_platform`.
- Homebrew is macOS-only. Linux has no Teams desktop cache to read.
