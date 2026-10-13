---
name: m365crawl
description: Use when an agent needs to read the user's Teams chats, Outlook mail, calendar, meeting transcripts, OneNote indexed notes, native people evidence or Office document metadata through the local `m365crawl` CLI and shared archive.
---

# m365crawl

Native people evidence: run `m365crawl people sync` once for supported Mac Outlook HxStore profiles, then `m365crawl people --query "name or address"` and `m365crawl people show person-observation:ID`. Use `people sources` to inspect partial/failed coverage. Explicit copied HxStore profile roots work with `people sync --root DIR`; Windows OWA people discovery does not. Native pair claims and profile-owner links do not establish subject tenancy or current contacts; identities are not merged by email, and conflicts stay unresolved. `--account` excludes unqualified native subjects with a notice. `--max-age 0` does not capture. Use exact independently qualified identity/account evidence for further mail/chat queries, never a guessed directory match.

m365crawl mirrors Microsoft 365 on this machine (Teams chats and channels, new Outlook cached mail on Mac and Windows, and the Teams calendar plus Outlook calendar on Mac) into one local SQLite archive, and answers from it offline and read-only. It cannot send, reply, react or mark anything read. It knows only what the desktop apps cached, so old history can be missing and counts are not server totals. `m365crawl skill` prints this guide for the installed version; SPEC.md in https://github.com/ourostack/m365crawl is the full contract.

## Privacy

- The archive holds the user's private chats and mail. Quote from it only where the user asked; never paste it into issues, logs or other tools unasked.
- Sign-in credentials are never read, and token-shaped values are stored as `[redacted]`.
- Never edit Teams' or Outlook's folders, or the archive, by hand; use the commands.
- `transcripts fetch` and `transcripts signin` are the only commands that use the network, through Edge (or Chrome) with m365crawl's own browser profile; every other command reads the archive offline. When `transcripts fetch` fails with `transcripts_signin_required`, ask the user before running `m365crawl transcripts signin`: it opens a visible Edge window where they sign in to Microsoft 365 once. Run it only after they say yes, then pass `--user-agreed` and relay the steps it prints. If the window asks to enroll or register this device, the organization signs in only from managed devices: tell the user, do not run it again, and keep using the commands that read the archive offline.

## Start here

Run `m365crawl` first: it reports chats, mail and calendar freshness, routes registered native people to independent coverage, and never syncs. Keep an explicit `--db` when following a route. Then read `m365crawl --help` and each command's `--help` for flags, ordering and fields; this guide does not repeat them.

- Output is JSON when stdout is not a terminal; pass `--json` to be sure. An error is one JSON line on stderr with a `fix`: follow it.
- Read commands sync first when the archive is older than `--max-age` (default 15m); `--max-age 0` reads it as it is.
- Lists stop at `--limit`, so check `truncated`. An empty list carries a `note` saying why: no archive, filters that matched nothing, or a range outside what the apps cached. `needs_sync` means no data yet, not no match.
- Unknown is not none. `coverage_gap` on a calendar result means some days are not cached, so a missing meeting is not evidence; a field named in `unknown_fields` was never stated. The calendar is Teams' cached copy: an event created or changed after an account's `teams_cache_fresh_at` (in `accounts`; a `notices` line says so when the range starts after it) is not listed until Teams refreshes it, so ask the user to open the Calendar in Teams, then sync again.
- `unread` and `messages --unread` cover chats and meetings; channels are left out (`channels_excluded`) because most are never opened. Add `--include-channels` to count them; channel mentions and replies reach you through `activity`.
- `messages` and `search` text output has a `thread` column that names the whole thread: for a chat message the two arguments of `m365crawl thread <conversation_id> <root_id>`, where the root is the message's `reply_chain_id`, else `id` (a channel reply's own id reads only that reply); for mail the id for `m365crawl mail thread <id>`.
- Any Teams link the user pastes (channel, chat, message or meeting) can be passed where a conversation or thread is expected: `--conversation '<link>'`, `thread '<message link>'`, `transcripts '<meeting link>'`; a channel or chat link to `thread` is an error, so use `messages --conversation`.
- `--fields` and `--max-text` keep results small, for example `m365crawl search "budget" --fields source,id,sent_at,subject,text --max-text 300`. Each list command's `--help` ends with the `--fields` keys it accepts; read it instead of guessing (a Teams message's text is `text`, an event's body is `body_text`, only in `calendar event`).

## Jobs

| Job | Commands |
| --- | --- |
| Read a chat or channel | `m365crawl conversations --query <name>` to find it, then `m365crawl messages --conversation <id> --since 7d`. Each Teams message carries `app_observation` (its own app, reply and citation claims, not a verified identity) and results add `application_evidence_coverage`; not derived means run a writable `m365crawl sync` |
| What someone said in a chat yesterday | `m365crawl messages --conversation <id> --from <name> --since <date>` (yesterday as YYYY-MM-DD), or `m365crawl search --from <name> --since 1d` across every chat |
| Mentions of me | `m365crawl search --mentions-me --since 7d` (`--direct-mentions` leaves out @channel and @team), `m365crawl activity --unread` |
| Triage: what needs me | `m365crawl unread --by-conversation --since 7d`, `m365crawl mail unread`, `m365crawl activity --unread` |
| Recall: what was said about something | `m365crawl search "words" --since 30d`, `m365crawl mail list --from <name>`, `m365crawl mail folders` (how far back mail reaches) |
| Threads: the whole conversation | `m365crawl thread <conversation_id> <root_id>` (the root is `reply_chain_id`, else `id`), `m365crawl mail thread <id>` |
| Cross-source and meeting prep | `m365crawl search "words"` (chats and mail), `m365crawl calendar`, then `m365crawl calendar event <event_id>`: one meeting with its `chat` (its newest messages in `chat.recent_messages`, kept by `--fields chat`) and `related_mail`, matched by invite or by subject from 14 days before to 7 days after the start; `m365crawl people --query <name>` finds Teams people and mail correspondents (`id` `mail:<address>`) |
| Attachments | `m365crawl mail list --has-attachments`, `m365crawl mail show <id>` (each file's name, size and type) |
| Meeting transcripts | `m365crawl transcripts` lists recorded meetings and which parts' text is in the archive; `m365crawl transcripts show <meeting>` prints one in time order with each seam marked; `transcripts fetch` brings the text from SharePoint; `m365crawl search "words" --source transcripts` finds what was said (`--from` matches the speaker); `calendar event` lists an occurrence's recorded calls in `transcripts` |
| Indexed notes | Explicitly run `m365crawl notes sync` once for available Mac indexes, or `notes sync --root DIR` for supported rollback-mode index copies. Then `notes search "words"` or shared `search`, followed by `notes show note:ID`; inspect `notes sources` before treating missing results as absence. |
| Document and cached Loop evidence | Explicit `docs sync`, `docs sync --root DIR` or `docs sync --registry-file FILE` registers qualified ordinary local Office JSON and Office registry files; registry rows carry the registry's own claims and opened time, never a modification time, so `--since`/`--until` skip them. Shared search or `docs search "words"` returns `document-observation:ID` for `docs show`; inspect `docs sources` before interpreting absence. Per-command help defines clocks, retained scope and refusals; the guide is optional. Metadata is not original text, resolved owner/residency or a new sharing event. Never dereference native URLs/friendly paths as qualified local files. Cached Loop channels: Explicit `loop sync` registers and projects cached Loop snapshot records already in the archive, 128 new records per run (rerun while `discovery_remaining` is true); nothing is fetched. `loop search "words"` or shared `search` returns `loop-channel:ID` for `loop show`; inspect `loop sources` (and its `retry`) before interpreting absence. Rows are partial channel text of one immutable snapshot version (`modified_at` null), not a full or current page; check `matches_retained_value`, `retained` and `pending_operations`. Decode `sequence` values as exact integers. |

## Indexed notes and source coverage

Notes are the local indexed cache, not a complete notebook export or original page layout. They preserve native text/OCR, elements, flags and indexed hierarchy. Partial/failed capture retains last-good representations; missing pages are retained. `source_coverage` survives `--fields`, truncation and zero hits; look at source status, losses and nullable attempt/admission/complete clocks. A failed automatic refresh may serve retained text with an explicit `refresh_error`.

Ordinary reads refresh **registered** participating locators only; new indexes need explicit `notes sync`. `--max-age 0` disables capture. Notes-only commands and `search --source notes` do not require Teams/Outlook or certify their freshness. `--source ID` on notes list/search chooses an archive-local source; it does not identify a user. Native notebook evidence never proves account ownership.

Shared search includes notes only without account, author, chat-only or mail-only constraints. Those constraints exclude unresolved notes with a notice; explicit `--source notes` rejects them. Date filters use native modified time, with unknown timestamps excluded. Expand note hits with `notes show note:ID`, never `thread`, invented authors or guessed page web URLs. Default Windows app-cache discovery and live WAL capture remain unsupported; supported explicit index copies work on Mac, Windows and Linux.

## Hosts

- macOS: `brew install ourostack/tap/m365crawl` or the release tarball. The app that runs m365crawl needs Full Disk Access; `m365crawl doctor` names it.
- Windows: the release zip. Its binaries are intentionally unsigned. Teams chats, the Teams calendar and new Outlook OWA cached mail work; mail has ordered To/Cc/Bcc roles and conversation grouping. Mac recipient roles are unknown/null. OWA calendar is not read in this release; Windows OWA calendar support is planned. Windows attachments are metadata only (downloaded=false, missing size=0), to_me=false and in_reply_to=null; unknown HasAttachments is not inferred, cached HTML may be truncated, and multiple content mailboxes per profile are a coded refusal. The mail group's help states these limits.
- Homebrew is macOS-only. Linux has no Teams desktop cache to read, but explicit copied supported OWA sources work. Explicit HxStore mail on Windows retains `mail_unsupported_platform`; that legacy route does not describe native OWA mail.
