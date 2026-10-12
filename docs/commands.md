# Command reference

This page lists every m365crawl command and flag, with the shape of each result. `m365crawl <command> --help` prints the same flags from the installed binary. The normative behaviour is in [SPEC.md](../SPEC.md).

Start with `m365crawl` (the overview) and `m365crawl --help`. Commands fall into ten groups:

| Group | Commands |
| --- | --- |
| Overview and health | [`m365crawl`](#overview), [`doctor`](#doctor), [`status`](#status), [`whoami`](#whoami), [`sync`](#sync), [`watch`](#watch) |
| Across sources | [`search`](#search), [`people`](#people), [`sql`](#sql) |
| Teams | [`messages`](#messages), [`unread`](#unread), [`thread`](#thread), [`conversations`](#conversations), [`teams`](#teams), [`activity`](#activity), [`stores`](#stores), [`records`](#records) |
| Mail | [`mail list`](#mail-list), [`mail show`](#mail-show), [`mail thread`](#mail-thread), [`mail folders`](#mail-folders), [`mail unread`](#mail-unread) |
| Calendar | [`calendar`](#calendar), [`calendar event`](#calendar-event), [`calendar actions`](#calendar-actions), [`calendar sources`](#calendar-sources) |
| Meeting transcripts | [`transcripts`](#transcripts), [`transcripts show`](#transcripts-show), [`transcripts fetch`](#transcripts-fetch), [`transcripts signin`](#transcripts-signin) |
| Indexed notes | [`notes sync`](#notes-sync), [`notes list`](#notes-list), [`notes search`](#notes-search), [`notes show`](#notes-show), [`notes sources`](#notes-sources) |
| Document metadata | [`docs sync`](#docs-sync), [`docs list`](#docs-list), [`docs search`](#docs-search), [`docs show`](#docs-show), [`docs sources`](#docs-sources) |
| Cached Loop channels | [`loop sync`](#loop-sync), [`loop list`](#loop-list), [`loop search`](#loop-search), [`loop show`](#loop-show), [`loop sources`](#loop-sources) |
| Tooling | [`metadata`](#metadata), [`skill`](#skill), [`version`](#version) |

## Platform defaults

| Host | Default `--teams-root` | Default `--outlook-root` | Default `--db` |
| --- | --- | --- | --- |
| macOS | `~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView` | `~/Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles` | `~/.m365crawl/m365crawl.db` |
| Windows | `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView` | `%LOCALAPPDATA%\Microsoft\Olk\EBWebView` | `%LOCALAPPDATA%\m365crawl\m365crawl.db` |

On Windows m365crawl reads Teams chats, the Teams calendar and new Outlook OWA cached mail. Mail commands, search, overview, status and doctor share that source. Explicit supported copied OWA roots work on all supported hosts; explicit HxStore roots retain their legacy behavior, including coded unavailable mail on Windows. OWA calendar is not read in this release; Windows OWA calendar support is planned. See [Windows mail differences](outlook-store.md#windows-owa-cached-mail).

On Windows the default archive path is private by construction. A custom `--db` path is allowed only when its direct parent directory is already private to the current user and SYSTEM, or m365crawl can create that parent itself as a new private directory. If that parent already exists and is not private, or the archive file already exists and is not private, open fails with `db_error` before SQLite writes anything.

## Global flags

Every command accepts these. `--fields` and `--max-text` apply to the list and read commands only (`search`, `messages`, `conversations`, `teams`, `people`, `activity`, `stores`, `records`, `unread`, `thread`, `watch`, every `mail` command, `calendar`, `calendar event`, `calendar actions`, `transcripts`, `transcripts show`, and `loop list`, `loop search`, `loop show` and `loop sources`); on any other command they are a `usage` error.

| Flag | Meaning |
| --- | --- |
| `--format=text\|json\|log` | Output format: text, json or log. Default: text on a terminal, json otherwise. |
| `--json` | Alias for `--format json`. |
| `--db=PATH` | Archive database path (`$M365CRAWL_DB`). Default: see the table above. |
| `--teams-root=DIR` | Teams EBWebView directory (`$M365CRAWL_TEAMS_ROOT`). Default: the new Teams container. |
| `--outlook-root=DIR` | New Outlook cache root (`$M365CRAWL_OUTLOOK_ROOT`): Mac profiles for mail/calendar or Windows Olk EBWebView for OWA mail only. Explicit supported OWA copies work portably; explicit HxStore roots preserve legacy behavior. `none` turns Outlook off, mail included. Outlook is off when `--teams-root` is set without this flag. A default absent source does not fail a Teams sync. A named directory that does not exist is `outlook_root_missing` (exit 3) on `sync` and implicit-sync read commands except `whoami`, `status` and `version`. Source decoding failures retain coded errors; doctor reports metadata/path/archive state without opening source values. |
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
- **Freshness.** Legacy read results carry `archive_age_seconds`, counted from the last fully successful sync of the accounts they cover. An archive with no complete legacy sync adds `"needs_sync": true` and `"hint": "run m365crawl sync"`. Notes have independent `source_coverage`; notes-only commands/search do not consult or certify Teams/Outlook freshness.
- **Text times** of day from the archive are local time followed by the zone's name, for example `2026-10-12 09:30 PDT` or a transcript part's `10:02 PST` (a zone without an abbreviation prints its offset name, such as `+03`). Date-only values carry no zone, and `watch` prefixes its own lines with this machine's clock. JSON times are UTC, so a text day can differ from the JSON day.
- **Log format.** `--format log` prints one `label=JSON` line, where the label is the command path with `_` for spaces, for example `docs_list={...}`. `skill` and `metadata` print their document in every format.
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
- **documents:** Office document observations, registered sources and failed sources, with the last admitted capture. A failure note points to `docs sources`.
- **notes:** OneNote indexed notes, registered sources and failed sources, with the last admitted capture.
- **loop:** cached Loop channel observations, registered sources and failed sources, with the last projection. With nothing registered it is `not_registered` and counts the archived cache records (`archived_records`) a `loop sync` would register. Failed and metadata-only sources (`metadata_only_sources`) point to `loop sources`; archived records not yet registered point to `loop sync`.
- **people:** Teams people and mail correspondents, and native Outlook people observations with their registered sources. With native sources it points to `people sources`.
- **transcripts:** recorded meetings, transcript parts and how many are fetched, with the last fetch.

These rows come from what the archive already holds; the overview reads and captures nothing for them. Under `--account`, the people row counts only that account's tenant's Teams people; mail correspondents, documents, notes, Loop channels, native people observations and transcripts are not narrowed by `--account`, and each such row says so in its note. Documents, notes, Loop channels and native people stay unnarrowed because their ownership is unresolved.

`--json` gives `{"archive_path", "archive_exists", "sources": [...], "next": [...], "note"?}`. Each source is `{"source": "chats"|"mail"|"calendar"|"documents"|"notes"|"loop"|"people"|"transcripts", "state", "conversations"?, "messages"?, "events"?, "newest_at"?, "oldest_at"?, "window_start"?, "window_end"?, "coverage_gap"?, "documents"?, "notes"?, "people"?, "observations"?, "calls"?, "parts"?, "fetched"?, "registered_sources"?, "failed_sources"?, "channels"?, "metadata_only_sources"?, "archived_records"?, "last_sync_at", "note"?}`. `last_sync_at` is the last sync for chats, mail and the calendar, the last admitted capture for documents, notes and native people, the last projection for Loop, and the last fetch for transcripts. `state` is `ok`, `empty`, `no_archive`, `not_registered` (documents, notes or Loop with no registered source; the note names the sync command), `unsupported` (a genuinely unsupported operating system), `off` (the Outlook source is off for this run) or `no_profile` (no supported Outlook profile on this machine). Windows OWA mail is supported. `next` lists the "Start here" commands that apply on this operating system, each as `{"command", "does"}`.

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
  m365crawl docs list         Office recent, shared and recommended document metadata; docs sync registers the sources
  m365crawl notes search "words"   OneNote indexed notes; notes sync registers the indexes
  m365crawl loop search "words"   cached Loop channel text (partial snapshots); loop sync registers the archived records
  m365crawl people            Teams people, mail correspondents and native Outlook people observations
```

## Document metadata

Office recent, shared, recommended and dialog observations join the archive as **metadata**, not document text, resolved file identities or sharing events. Start from `m365crawl docs --help`; help, IDs and coded remedies are sufficient without a skill. Mac default discovery uses named Office metadata inputs; `--root DIR` supplies the four finite JSON filenames on a qualified local disk. Windows requires explicit ordinary fixed-drive NTFS copies; native Office discovery is unsupported. Linked, provider/placeholder, remote and unqualified paths are refused. No native URL or friendly path is opened or fetched.

Every read/source result carries `source_coverage` outside `--fields`, item bounds and zero hits. `scope` is `office_json_metadata`, with `state` unknown or registered and per-source status, losses and nullable clocks. Complete means loss-free captured metadata, not library/service completeness. Partial, failed and absent later inputs retain old observations; unchanged capture does not repair partial coverage or alter original clocks. Ordinary reads refresh registered locators only; new registrations require explicit sync. `--max-age 0` disables capture and old read-only archives remain unchanged. Document-only commands ignore Teams/Outlook freshness and irrelevant root paths.

Terms, as `docs --help` defines them:

- Surfaces: `dialog` is the app's local Open-dialog recent list (often `file://` paths); `recent` is Office's Recent list of files the account opened; `recommended` is Office's Recommended list, with its activity badge; `shared` is Office's Shared with me list, with the sharer and date Office reported. Application `supplied` means a file given with `--root`.
- `association_state` `unresolved`: the row is not linked to a file, owner, account, chat or mail. `boundary` `unresolved` (sources): a source file is not proven to belong to one account.
- native `Sharing.Type` is Office's SharingType number as written; m365crawl does not map it.
- Clocks: `modified_at` is the native document modification time Office reported, null when Office gave none or it did not parse (`Raw` keeps the text); `captured_at` is when m365crawl first copied the file holding that exact row; `source_observed_at` is that file's modification time on disk when copied.
- Order of `docs list` and `docs search`: `modified_at` newest first, null last, then title (ASCII case-insensitive), source ID and observation ID.

### docs sync

```sh
m365crawl docs sync [--root DIR | --source-id ID]
```

Explicit registration/capture. A supplied directory contains `recent.json`, `shared.json`, `recommended.json` and/or `dialog.json`; other files are ignored. `--source-id` refreshes only one positive registered source and cannot combine with `--root`. Returns `captured`, `unchanged`, `skipped`, `failed` and `source_coverage`. Unrecognized or unsafe peers remain source failures, not empty successes. A successful Office response that lists no documents, in one of the two empty shapes observed in real files, is a complete capture with zero rows and reason `office_response_listed_no_documents`, explained by a `source_coverage.notices` entry; earlier observations from that source stay in the archive with `retained` true. The reason describes that saved response, not the account's documents in the cloud. The recent empty shape has exactly `response_code` 1, a non-empty `delta_token` and `diagnostics_data` with `http_status_code` 200. The recommended empty shape has exactly `response_code` 0, an empty `error_data`, that same `diagnostics_data`, an empty `prefetch_documents_group` and a `documents_group` holding only `title`, `ranker` and `digest`. Any other top-level key, any other code, and every shared file without documents still fail as `office_json_unrecognized`. A dialog list (`[]`) or a supplied `--root` file that lists no documents is not an Office service response, so its complete zero-row capture carries reason `office_json_listed_no_documents` and a notice that describes only the saved file. Directory, archive and cancellation errors remain command failures.

### docs list

```sh
m365crawl docs list [--source-id ID] [--query WORDS] [--application APP] [--surface SURFACE] [--since TIME] [--until TIME] [--limit N]
```

`--application` takes `Word`, `Excel` or `PowerPoint` in any case, or `supplied` for files given with `docs sync --root`; `--surface` takes `recent`, `shared`, `recommended` or `dialog`. Any other value is a usage error that names the accepted values. Both filters narrow the observation rows only; `source_coverage` still lists every document source and its notices.

One row per retained native representation, with stable `document-observation:ID`, source/capture IDs, immutable original clocks, application/surface, title, URLs, native claims, retained state and unresolved association/not_captured text state. Order is native modification descending, unknown last, then full ASCII-NOCASE title/source ID/numeric rowid before LIMIT. Limit defaults50; zero returns all. Date filters use native modification, never acquisition time, excluding unknown dates. Display bounds preserve IDs and URLs. `native` uses source-model field names such as `Title`, `ResourceID`, `Sharing` and timestamps `Raw`/`Value`; Value is parsed UTC or null when missing/unrecognized. `captured_at` is original private acquisition; `source_observed_at` is the acquired metadata file's modification time, not document or event time. `retained` means absent from the last admitted capture, not deleted or sharing revoked.

`--fields` keys: `id`, `source_id`, `capture_id`, `application`, `surface`, `title`, `url`, `web_url`, `native`, `modified_at`, `captured_at`, `source_observed_at`, `retained`, `association_state`, `text_state`, `text_truncated`.

### docs search

```sh
m365crawl docs search WORDS [--source-id ID] [--application APP] [--surface SURFACE] [--since TIME] [--until TIME] [--limit N]
```

Search native metadata strings, not document contents or JSON property names. Words are optional with a date, `--application` or `--surface` filter. The same list ordering, limits and fields apply. Shared `search --source documents` returns metadata-only hits and follow-up IDs; unconstrained default search includes them alongside other sources. Account/author/chat/mail-only filters exclude unresolved metadata with a notice; explicitly selecting documents rejects those filters. No guessed owner, Teams thread or file path is a valid detail follow-up.

`--fields` keys: `id`, `source_id`, `capture_id`, `application`, `surface`, `title`, `url`, `web_url`, `native`, `modified_at`, `captured_at`, `source_observed_at`, `retained`, `association_state`, `text_state`, `text_truncated`.

### docs show

```sh
m365crawl docs show document-observation:ID
```

Read the native representation and up to100 exact URL reference occurrences, with `references_total` and `references_truncated`. References are unresolved evidence, not one resolved document or proof that a file is locally available. Lookup precedes refresh; only this observation's registered source participates, not peers/default roots. Missing/invalid IDs return a coded error pointing to `docs list`.

`--fields` keys: `id`, `source_id`, `capture_id`, `application`, `surface`, `title`, `url`, `web_url`, `native`, `modified_at`, `captured_at`, `source_observed_at`, `retained`, `association_state`, `text_state`, `text_truncated`, `references`, `references_total`, `references_truncated`.

### docs sources

```sh
m365crawl docs sources [--source-id ID]
```

Inspect source IDs, kind, admitted/attempted state, coverage, reason and losses. Reason `office_response_listed_no_documents` on a complete source means its last saved Office response listed no documents. Reason `office_json_listed_no_documents` means the same for a dialog list or a supplied `--root` file. `source_id` is null for a locator that has never had a capture admitted; `kind` and `locator_tail` (the path's last two elements) tell such rows apart, and the text table shows both. A failed or cancelled source carries `fix`, the remedy for its reason (a retry is plain `m365crawl docs sync`, because a source with null `source_id` cannot be targeted with `--source-id`), and `effect`, which says whether the last good capture stays current (its documents stay listed) or nothing was ever captured.

| Reason | Meaning and remedy |
|---|---|
| `office_json_unrecognized` | Not JSON, or JSON in a shape this version does not read. Upgrade; if the newest version still fails, open an issue at https://github.com/ourostack/m365crawl/issues asking for the shape, with the source kind and top-level key names (no values). |
| `office_json_too_large` | Larger than the reader accepts (8 MiB, or too many rows, fields or nesting levels); nothing was kept. Open an issue if Office wrote it. |
| `office_json_unreadable` | Could not be read to the end. Sync again; check that the file is complete. |
| `office_surface_unknown` | Registered for a surface this version does not read. Upgrade. |
| `office_json_input_unqualified` | Missing, a link, on a cloud or placeholder provider, remote, or not an ordinary local file; refused before reading. Copy it to an ordinary local folder and use `docs sync --root DIR`. |
| `office_json_capture_failed` | The private copy or its checks failed, often because the file changed while copied. Sync again; run `doctor` if it repeats. |
| `office_json_cleanup_failed` | The private temporary copy could not be removed. Check the temporary directory's space and permissions. |
| `office_json_policy_restore_failed` | A capture safety setting could not be restored. Sync again; open an issue if it repeats. |
| `office_json_cancelled` | Interrupted or timed out. Sync again. |
| `stale_attempt` | Another capture of the same source finished first. Run `docs sources`. | Never captures, discovers or creates an archive. An unknown positive source is an error rather than an empty success. Source account/subject ownership remains unresolved, so account/link/profile filters are rejected by the docs group.

`--fields` keys: `kind`, `source_id`, `locator_id`, `attempt_id`, `admitted_id`, `platform`, `locator`, `boundary`, `status`, `coverage`, `qualification`, `acquisition_signature`, `content_digest`, `reason`, `attempted_at`, `admitted_at`, `complete_at`, `source_observed_at`, `losses`, `locator_tail`, `fix`, `effect`.

## Cached Loop channels

Loop channels are text that m365crawl projects from cached Loop snapshot records Teams already wrote and `sync` already archived (database `fluidDriverCache`, store `driverStorage.V3`). Each row is one channel of one immutable cached snapshot: partial channel text, not a full or current Loop page, table, author or server history. m365crawl never fetches a component, replays operations or opens Loop. Start from `m365crawl loop --help`; help, IDs and coded remedies are enough without a skill.

- **IDs.** `source_id` is the stable registration ID of one archived record tuple (`source`, `database`, `store`, `key_json`). `loop sync` allocates it when it first sees the tuple, before any successful projection, so failed and metadata-only sources have one too. `snapshot_id` names one admitted version of that record (its exact value digest and reader qualification). `loop-channel:ID` names one channel of one snapshot and never moves to a newer channel.
- **Clocks.** `modified_at` is always null: the cache records no native modification time. `record_updated_at_at_admission` is the archive record's update clock when the snapshot was first admitted, and `projected_at` when it was first projected; neither is native edit time. `raw_updated_at` and `raw_removed_at` are the archived record's current clocks.
- **Match state.** `matches_retained_value` says the archived record's current bytes and reader version are exactly this snapshot's. `retained` says a newer admitted snapshot of the same source exists. `raw_present` false means the record left Teams' cache, not that anything was deleted. `loop_coverage.sources[].snapshot_match` is `no_admission`, `matches`, `differs` or `unavailable`.
- **Sequences.** `sequence`, `snapshot_sequence` and `latest_sequence` are native signed 64-bit numbers, printed exactly in JSON, `--fields` and text; decode them with an exact-integer parser. `pending_operations` counts cached operations m365crawl did not apply; zero still does not mean the text is current.
- **Freshness.** Ordinary Loop reads refresh registered sources whose last attempt is older than `--max-age`; `--max-age 0` and `loop sources` only read the archive and never create one. New tuples are registered only by `loop sync` without `--source-id`. Loop commands ignore Teams/Outlook freshness and refuse `--account`, `--outlook-account` and `--outlook-profile`.
- **Coverage.** Every Loop result carries `loop_coverage` outside `--fields`, `--limit` and zero hits: `scope` `loop_cache_channels`, `state` `not_registered` or `registered`, `totals` (`registered`, `failed`, `metadata_only`, and the sources by `snapshot_match`: `matches`, `differs`, `unavailable`, `no_admission`), `sources`, and `refresh_error` and `diagnostics` when a refresh reported them. Loop commands list each source in full, as `loop sources` does. Shared `search` lists each source compactly: `source_id`, `state` (the status, or `metadata_only`), `snapshot_match`, `raw_present`, `reason` and `inspect` (`m365crawl loop sources --source-id N`), with no tuple.
- **Read cost.** A read compares each registered record's stored fingerprint (the archive's content hash, value length, update clock, raw digest and redaction state at the last admission) and loads and hashes a value only when that differs. An unchanged source costs no value load, and a source that was never admitted is not loaded either. A read with nothing due checks attempt times only and then reads the sources once, with its rows.
- **Overview.** Bare `m365crawl` has a `loop` row: archived records, registered, failed and metadata-only sources, channels and the last projection.
- Order of `loop list` and `loop search`: full channel path (ASCII case-insensitive), source ID, snapshot ID, then channel ID, before `--limit`.

### loop sync

```sh
m365crawl loop sync [--source-id ID]
```

Explicit registration and projection from the archive only. `--source-id` refreshes only that registration. Without it, `loop sync` refreshes every registered source, then registers and projects new archived records in numeric record order, at most 128 per run. Each tuple is registered before its value is read, and one value at a time is loaded, parsed, committed and released.

- **More than 128 new records is not an error.** The run registers the first 128, exits 0 and returns `discovery_remaining` true, `discovery_remaining_records` (the unregistered records it did not reach) and a `notice`: run `m365crawl loop sync` again to continue. Each run takes the next 128.
- **Records that cannot be registered never block the rest.** A record whose identity or owner fields are outside the reader's bounds, or not UTF-8, is skipped and does not count toward the 128. `invalid_records` counts them, and `loop_coverage.diagnostics` names the first 16 as `loop_cache_identity_unqualified: records rowid N` and counts the rest. No archived string is printed. Inspect one with `m365crawl sql "select database, store, octet_length(key_json) from records where rowid=N"`.
- **Result.** `projected`, `unchanged`, `skipped`, `failed`, `invalid_records`, `discovery_remaining`, `discovery_remaining_records`, `notice` (only when more remain) and `loop_coverage`.
- **Failures.** A source that cannot be read or parsed is still registered, recorded as failed with a fixed reason, and keeps its last good snapshot and its peers. Archive and cancellation errors are command failures; if the run had already written, the result on stdout still reports what it committed and the error follows on stderr.

### loop list

```sh
m365crawl loop list [--source-id ID] [--query WORDS] [--limit N]
```

One row per retained channel observation, current and retained snapshots alike. `--query` matches words in channel text or path. Limit defaults to 50; 0 returns all. `--max-text` bounds `text` and `path` and sets `text_truncated` and `path_truncated` without changing IDs.

`--fields` keys: `id`, `source_id`, `snapshot_id`, `file_id`, `path`, `path_truncated`, `text`, `text_truncated`, `sequence`, `snapshot_sequence`, `latest_sequence`, `markers`, `pending_operations`, `snapshot_pending_operations`, `missing_blobs`, `partial`, `losses`, `modified_at`, `record_updated_at_at_admission`, `projected_at`, `raw_updated_at`, `raw_removed_at`, `raw_present`, `matches_retained_value`, `retained`, `snapshot_tenant_id`, `snapshot_user_id`, `association_state`, `text_state`.

### loop search

```sh
m365crawl loop search WORDS [--source-id ID] [--limit N]
```

Search channel text and paths; words are required. The same ordering, limits and fields as `loop list` apply. Shared `search --source loop` returns the same rows with `source` `loop`; unconstrained `search` includes them after every dated hit. Date, account, author, conversation, team, folder, mention, deletion, HTML and profile filters cannot qualify Loop channels: `--source loop` refuses them with `flag_source_conflict`, and `--source all` leaves Loop out with a notice.

`--fields` keys: `id`, `source_id`, `snapshot_id`, `file_id`, `path`, `path_truncated`, `text`, `text_truncated`, `sequence`, `snapshot_sequence`, `latest_sequence`, `markers`, `pending_operations`, `snapshot_pending_operations`, `missing_blobs`, `partial`, `losses`, `modified_at`, `record_updated_at_at_admission`, `projected_at`, `raw_updated_at`, `raw_removed_at`, `raw_present`, `matches_retained_value`, `retained`, `snapshot_tenant_id`, `snapshot_user_id`, `association_state`, `text_state`.

### loop show

```sh
m365crawl loop show loop-channel:ID
```

Resolve the stored channel first, refresh only its own source, then return the same immutable channel with its snapshot versions, losses and source state. A newer snapshot never replaces it; the channel then has `retained` true. A missing or malformed ID is `loop_channel_unknown` or `usage`, pointing to `loop list`.

Text output adds a detail block under the row: `text state: partial_snapshot`, the source, snapshot and file IDs, the channel, snapshot and latest sequences, pending operations, markers, missing blobs, losses, whether the archived record still matches, and the admission, projection and raw clocks in local time with the zone's name, then the channel text. `loop list` text stays one row per channel.

`--fields` keys: `id`, `source_id`, `snapshot_id`, `file_id`, `path`, `path_truncated`, `text`, `text_truncated`, `sequence`, `snapshot_sequence`, `latest_sequence`, `markers`, `pending_operations`, `snapshot_pending_operations`, `missing_blobs`, `partial`, `losses`, `modified_at`, `record_updated_at_at_admission`, `projected_at`, `raw_updated_at`, `raw_removed_at`, `raw_present`, `matches_retained_value`, `retained`, `snapshot_tenant_id`, `snapshot_user_id`, `association_state`, `text_state`.

### loop sources

```sh
m365crawl loop sources [--source-id ID]
```

Inspect each registration: its exact `tuple`, `status` (`complete`, `partial`, `failed`, `cancelled`), `coverage` (`channels`, `partial`, `metadata_only`), `reason` (the latest attempt's), `raw_reason` (why the archived record could not be read now, if it cannot), `snapshot_match`, the current and admitted owner IDs, attempt and admission clocks, `losses` and `retry` (`m365crawl loop sync --source-id N`). A failed, cancelled or metadata-only source adds `fix` and `effect`. It never projects, registers or creates an archive. An unknown positive source is `loop_source_unknown`.

| Reason | Meaning and remedy |
|---|---|
| `loop_cache_identity_unqualified` | The record's identity or owner fields are outside the reader's bounds or not UTF-8, so they are not read or shown. A registered source fails with it when its archived record changes that way: its `current_tenant_id` and `current_user_id` are then empty and its last good snapshot stays. An unregistered record with it is never registered and appears only as a `loop sync` diagnostic naming its `records` rowid. |
| `loop_cache_record_missing` | The archived record is gone. Nothing can be projected until Teams caches it again and `m365crawl sync` archives it. |
| `loop_cache_value_missing` | The archived record holds no value. Run `m365crawl sync`, then retry. |
| `loop_cache_value_too_large` | The archived value is larger than 8 MiB, so it was not loaded. |
| `loop_cache_value_unqualified` | The archived value or its clocks are not in the text form the reader accepts. |
| `loop_cache_snapshot_unsupported` | The cached value is not a Loop snapshot shape this version reads. Upgrade, then retry. |
| `loop_cache_snapshot_too_large` | The snapshot exceeds the reader's blob, channel, segment or nesting limits. |
| `loop_cache_reader_failed` | The value could not be read or parsed. Retry, and open an issue at https://github.com/ourostack/m365crawl/issues if it repeats. |
| `loop_cache_input_changed` | The archived record changed while it was projected, usually because a sync ran at the same time. Retry. |
| `loop_cache_cancelled` | The projection was interrupted. Retry. |
| `stale_attempt` | Another projection of the same source finished first. Run `loop sources`. |

`--fields` keys: `source_id`, `kind`, `tuple`, `status`, `coverage`, `reason`, `raw_reason`, `attempt_id`, `admitted_attempt_id`, `incarnation_id`, `last_snapshot_id`, `snapshot_match`, `qualification`, `snapshot_qualification`, `raw_present`, `raw_value_present`, `raw_updated_at`, `raw_removed_at`, `current_tenant_id`, `current_user_id`, `snapshot_tenant_id`, `snapshot_user_id`, `attempted_at`, `admitted_at`, `losses`, `retry`, `effect`, `fix`.

## Indexed notes

Notes come from OneNote's local full-text indexes, not a complete notebook export. Default discovery is Mac's `~/Library/Containers/com.microsoft.onenote.mac/Data/Library/Application Support/Microsoft User Data/OneNote/15.0/FullTextSearchIndex`. Supported explicit rollback-mode `.db` index copies work on Mac, Windows and Linux; default Windows app discovery and live WAL capture are not supported.

Every notes list/show/search/source result carries `source_coverage: {scope: "onenote_indexed_cache", state, sources}` outside item shaping and zero-hit results. `state` is `unknown` with no registered capture, or `registered` with per-source status. Explicit sync of an empty directory reports `examined_empty_directory`. Per-source fields include source and locator IDs, attempted/admitted IDs, nullable attempt/admission/complete/source clocks, distinct acquisition signature and content digest, qualification, coverage, loss counts and a safe reason.

`complete` means loss-free **indexed** scope, not full notebook or service completeness. `partial` and failed/unavailable/cancelled attempts retain old page text, OCR, flags and stable IDs. An unchanged copy inherits prior losses and coverage; it does not repair a partial capture. Old notebook incarnations remain separately attributed as `retained`. A failed automatic refresh may serve retained content with explicit `source_coverage.refresh_error`; it never hides the failure as healthy empty data.

Ordinary reads refresh registered participating locators only, including previously registered explicit copies. `--source ID` selects one archive-local instance and its locator; a disappeared source remains visible with old content. New indexes require explicit `notes sync`. Recent same-input failed/partial attempts are throttled; changed metadata permits another attempt and explicit sync retries immediately. `--max-age 0` performs no capture. Notes do not use Teams/Outlook freshness or infer account ownership.

### notes sync

```sh
m365crawl notes sync [--root DIR]
```

Explicit local capture and registration, independent of Teams/Outlook. Result: `{"captured", "unchanged", "skipped", "failed", "source_coverage"}`. At most 64 `.db` inputs are examined. Each unsafe, malformed or unavailable peer is separately reported and cannot undo a healthy peer. Directory/discovery, archive and cancellation errors remain command failures.

### notes list

```sh
m365crawl notes list [--source ID] [--query TEXT] [--since TIME] [--until TIME] [--limit N]
```

Newest native modified time first, undated notes last, stable archive rowid for ties. Default limit 50. Date windows exclude unknown native times; `--until` is exclusive. Items expose stable `note:ID`, source/capture IDs, native GOID/GUID/GOSID and parents, title/text, nullable modified/captured/source-observed times. Text joins indexed rich text and OCR in native index-row order, not original page layout.

`--fields` keys: `id`, `source_id`, `capture_id`, `goid`, `guid`, `gosid`, `parent_goid`, `grandparent_goids`, `title`, `text`, `modified_at`, `captured_at`, `source_observed_at`, `text_truncated`.

### notes search

```sh
m365crawl notes search TEXT [--source ID] [--since TIME] [--until TIME] [--limit N]
```

The same result as filtered notes list. Searches title, text and OCR with literal FTS escaping, quoted phrases and prefix terms. Coverage is present even when no hit matches.

`--fields` keys: `id`, `source_id`, `capture_id`, `goid`, `guid`, `gosid`, `parent_goid`, `grandparent_goids`, `title`, `text`, `modified_at`, `captured_at`, `source_observed_at`, `text_truncated`.

### notes show

```sh
m365crawl notes show note:ID
```

One-item list with the note and native indexed `elements`, `flags`, `ancestors` and `ordering: "index_rowid_not_document_layout"`. Element IDs and their native parent rowids remain distinct from archive IDs. `--max-text` bounds combined text and each element separately, preserving native kind and IDs. Invalid or missing note IDs fail explicitly.

`--fields` keys: `id`, `source_id`, `capture_id`, `goid`, `guid`, `gosid`, `parent_goid`, `grandparent_goids`, `title`, `text`, `modified_at`, `captured_at`, `source_observed_at`, `text_truncated`, `ordering`, `elements`, `flags`, `ancestors`.

### notes sources

```sh
m365crawl notes sources
```

Lists source health and preserved capture evidence. A locator is a cache registration, never a guessed user/tenant. Notebook Type4 native identities qualify continuity; adding, removing or renaming pages does not create a new source. Verified conflicting notebook evidence creates a separate retained incarnation.

**Shared search:** `search --source notes` uses the independent notes path, without irrelevant Outlook checks or Teams sync. Default `search` includes registered notes alongside legacy sources. `--account`, `--from`, chat-only flags and mail `--folder` exclude unresolved notes with a notice; those flags are usage conflicts with explicit `--source notes`. `--since`/`--until` filter native modified time, not capture time. Each hit has `source: "notes"` and follows up through `notes show note:ID`, never a fabricated thread or author. Participating notes disable a chat-only `total`, even with zero note hits.

`source_id` is null until a capture of the locator is admitted; `kind` and `locator_tail` identify the row.

`--fields` keys: `kind`, `source_id`, `locator_id`, `attempt_id`, `admitted_id`, `platform`, `locator`, `boundary`, `status`, `coverage`, `qualification`, `acquisition_signature`, `content_digest`, `reason`, `attempted_at`, `admitted_at`, `complete_at`, `source_observed_at`, `losses`, `locator_tail`, `fix`, `effect`.

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
| `mail_readable` | A warning. Per Outlook profile, when its mail was last read or why the last read failed; unreadable OWA profiles are not treated as an empty successful mailbox. Mail is not refreshed when the Outlook source is off. |
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

Result: archive path, schema version, per-account counts, the last run and other Teams origins seen, and a `mail` block: `{"messages", "unread", "folders", "oldest_at", "synced_at", "state", "source_error"?}`. `messages` counts mail that is not gone or evicted. `state` is `ok` once a sync has read mail, `skipped` when the Outlook source is off for this run or no sync has read mail yet, `no_profile` only after inspecting an empty source or finding a resolved optional default absent, and `unsupported_platform` on a genuinely unsupported operating system. Failed root resolution, enumeration or a refused profile gives additive state `unavailable` and `source_error: {code, message, fix}` with fixed `outlook_profile_unreadable` diagnostics; archived counts remain visible and the optional source does not fail a read-only status command.

Overview likewise reports mail `unavailable` with a fixed note, preserving known archived counts. Empty mail and search results retain that source diagnosis rather than claiming no supported profile or an empty mailbox. Healthy archived search results remain visible alongside an unreadable-sibling note. Discovery metadata alone never proves whether a mailbox contains messages; doctor does not decode source values. Turning the source off still reports `skipped`/`off`, and a resolved absent optional default remains `no_profile`.

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

Result: the sync report (SPEC.md sections 4 and 5), with one entry per source in `sources`. A Teams source carries counts for `conversations`, `messages`, `people`, `activity`, `records` and `calendar` (`events`, `recaps`, `recap_items`, `gone`, `linked`, `refused` and the link counts). HxStore profiles have separate calendar and mail sources; OWA profiles contribute `outlook|<profile>|mail` only, with `mail: {added, updated, replaced, gone, evicted, withheld}` counts. A mail failure does not fail another source's progress. When an archive has messages whose application evidence was never projected under the current mapper version, the sync projects them offline first and reports `application_backfill` (`mapper_version`, `processed`, `failed`, `absent`); a failed backfill fails the sync before any capture and the next sync retries it. Explicit Windows HxStore mail retains its `unavailable`/`mail_unsupported_platform` result; native OWA is supported and decoding failures are coded errors, not successful fallbacks.

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

Full-text search over Teams chats, Outlook mail, fetched meeting transcripts, participating registered indexed notes/document metadata and cached Loop channels, newest first; default `--limit 50` (check `truncated`). Knowledge hits use native modification time, with unknown times last; document hits remain metadata-only unresolved observations.

```
m365crawl search [<query>] [flags]
```

| Argument | Meaning |
| --- | --- |
| `[<query>]` | Words to find; "quoted phrases" and a trailing `*` for prefixes are supported. Optional when a filter (`--mentions-me`, `--direct-mentions`, `--from`, `--conversation`, `--team`, `--folder`, `--since`, `--until`) is given: then the filters alone select the messages. |

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--source=chats\|mail\|transcripts\|notes\|documents\|loop\|all` | every source | Choose messages, fetched transcripts, indexed notes, document metadata, cached Loop channels or all participating sources (default). |
| `--from=STRING` | chats, mail, transcripts | Sender or transcript speaker; unresolved notes/documents are excluded from `all` and reject this filter when explicitly selected. |
| `--since=STRING` | every source | Only messages at or after this time: RFC3339, YYYY-MM-DD (local midnight) or a relative duration (90m, 24h, 7d, 2w). For transcripts, the time of each entry. |
| `--until=STRING` | every source | Bound source-native time (same formats as `--since`). Document modification timestamps are exclusive upper bounds; a date-only document bound includes its whole local day. Unknown knowledge times do not match bounded windows. |
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

- Every item has `source`: `chats`, `mail`, `transcripts`, `notes`, `documents` or `loop`. A chat item has the shape of a `messages` item; a mail item has the shape of a `mail list` item. Registered knowledge participates only when its unresolved scope permits the selected filters.
- A transcript item is one part of a recorded meeting whose fetched text matched: `{"source", "call_id", "event_key", "title", "ordinal", "speaker", "at", "text", "matches", "fetched_at"}`. `speaker`, `text` and `at` (absolute) are those of the part's first matching entry, and `matches` counts the part's matching entries, so a part that says a word fifty times is one item. The words match what was said, not the speaker's name (that is `--from`). Only text already fetched into the archive is searched, offline; with `--source transcripts` and nothing fetched, `note` says `no transcripts are fetched yet; run m365crawl transcripts to see what can be fetched`.
- Knowledge hits have stable `note:ID` or `document-observation:ID`; follow `notes show` or `docs show`, not a message thread. Document metadata remains unresolved, with `text_state=not_captured`; URLs are reference evidence, not resolved files or proof of local residency.
- Loop hits are `loop list` rows plus `source`, with a stable `loop-channel:ID` to follow with `loop show`. They have no native date (`modified_at` null), so they follow every dated hit in path order. `--source loop` needs words and refuses date, account, author, chat-only, mail-only and profile filters with `flag_source_conflict`; `--source all` with any of them leaves Loop out with a notice. `loop_coverage` carries totals and one compact row per participating Loop source (`source_id`, `state`, `snapshot_match`, `raw_present`, `reason`, `inspect`) outside `--fields`, `--limit` and zero hits; `loop sources` has the full detail. A mixed result never advertises a chat-only `total`. Loop is refreshed after the implicit sync, so it projects the records that sync archived.
- Text output names source-specific follow-ups: `thread <conversation_id> <reply_chain_id, else id>` for chats, `mail thread <id>` for mail, `transcripts show <call_id>` for transcripts, the corresponding knowledge `show` command for notes/documents, and `loop show <id>` for Loop channels.
- Items are newest first by `sent_at` for chats, `received_at` for mail, `at` for transcripts and native `modified_at` for knowledge; unknown knowledge dates sort last.
- The result's `sources` reports count/truncation for each participating source. Knowledge `source_coverage` stays outside item shaping and zero hits; document rows and their source frontier come from one read snapshot, preserving independent refresh errors.
- With `--source all`, a flag that belongs to one source narrows the search to that source and `note` says so, for example `--mentions-me applies to Teams chats only; mail and meeting transcripts were not searched`. With another `--source`, the same flag is the usage error `flag_source_conflict`, which names the flag.
- `note` also says when mail is left out because it is not in the archive yet (`mail is not in the archive yet; run m365crawl sync`) or not read on this system.
- `--account` takes a Teams account (`<tenantId>/<userId>`), which narrows chats and searches all mail, or a mail account (`outlook/<profile>`), which narrows mail and skips chats and transcripts. It excludes unresolved notes/documents from `all`; explicitly selected knowledge rejects it. The other source's flag with it, or a `--source` it rules out, is a `flag_source_conflict`.
- Chat hits carry `app_observation` as in `messages`. `application_evidence_coverage` counts the chat hits that survive the merged `--limit`, so mail, transcript and knowledge hits never count, and a result without chat hits has coverage `unknown`.
- `--fields` accepts the keys of every kind of item, plus `source`.
- A query with a syntax error (an unbalanced quote) is one usage error, not one per source.

```sh
m365crawl search planning --limit 5
m365crawl search "budget review" --source mail --since 30d
m365crawl search --mentions-me --since 7d --max-text 200
m365crawl search "release date" --source transcripts --from ada
```

`--fields` keys: `source`, `tenant_id`, `id`, `source_id`, `capture_id`, `application`, `surface`, `title`, `url`, `web_url`, `native`, `modified_at`, `captured_at`, `source_observed_at`, `retained`, `association_state`, `text_state`, `text_truncated`, `user_id`, `conversation_id`, `conversation_display_name`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `app_observation`, `account`, `folder`, `folder_kind`, `to_me`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `received_at`, `is_read`, `flag`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `call_id`, `event_key`, `ordinal`, `speaker`, `at`, `matches`, `fetched_at`, `goid`, `guid`, `gosid`, `parent_goid`, `grandparent_goids`, `snapshot_id`, `file_id`, `path`, `path_truncated`, `sequence`, `snapshot_sequence`, `latest_sequence`, `markers`, `pending_operations`, `snapshot_pending_operations`, `missing_blobs`, `partial`, `losses`, `record_updated_at_at_admission`, `projected_at`, `raw_updated_at`, `raw_removed_at`, `raw_present`, `matches_retained_value`, `snapshot_tenant_id`, `snapshot_user_id`.

## people

List existing Teams people and mail correspondents alongside explicitly registered native Outlook observations. `last_seen_at` is the newest time the person appears in archived data: sent or was mentioned in a Teams message, was a member of a chat at its last message, or sent or received archived mail. It is not the last chat with you; use `conversations` (`last_message_at`) or `messages --conversation` for chat recency. Native observations have no value for that clock and remain unresolved source evidence.

IDs: `8:orgid:<guid>` is a person, `28:<id>` is a bot or app, `19:<id>@thread...` is a chat, channel or team named in a mention, `everyone` is the @everyone mention, `mail:<address>` is a mail correspondent, and `person-observation:ID` is a native Outlook observation. `--account` takes `<tenant_id>/<user_id>` as `whoami` (or `status`) lists the accounts.

An unscoped list carries a notice that says what it included: Teams people and mail correspondents of every account, plus native Outlook observations when people sources are registered, with the caveat that native subjects have unresolved account ownership.

```
m365crawl people [flags]
```

| Flag | Meaning |
| --- | --- |
| `--query=STRING` | Part of a display name or mail address, or an exact person id. A mail correspondent's id is `mail:<address>`, and a `--query` of that form matches that one address exactly. |
| `--limit=50` | Maximum items in the merged list; `truncated` is true when either source had more. |

Result: a list of person items, known `last_seen_at` newest first, then unknown times. Ties use full display names (ASCII case-insensitive), then source ID and numeric observation ID for native rows, otherwise person ID. `people --help` exposes the default list's query, limit and accepted fields. An empty combined/native list explains that no archived person matched and directs readers to independent source coverage; it is not evidence of contact completeness.

- **A Teams person** has `sources: ["chats"]` and no `email`, because Teams keeps no address for a person.
- **A mail correspondent** is a sender or recipient with an address; gone messages do not count. It has `id: "mail:<address>"`, `email` (the address, lower case), `sources: ["mail"]`, an empty `tenant_id`, and as `display_name` the newest name it was given, or the address when it never had one.
- **Sources are never silently merged**, so the same person can appear in several sources or native representations.
- `--account` narrows the Teams people to that account's tenant; mail correspondents are not narrowed.
- Registered native observations expose `person-observation:ID`, native class/key/version/framing, exact email/directory pair claims, separate refresh/capture/source clocks and ambiguity/conflicts. Account-filtered lists exclude those unqualified subjects with a notice; the source profile owner's account is not the subject's tenant.

`--fields` keys: `tenant_id`, `id`, `display_name`, `email`, `sources`, `last_seen_at`, `source_id`, `capture_id`, `native_class`, `native_key`, `native_parent`, `version_raw`, `resynced`, `first_name`, `last_name`, `alternate_email`, `teams_id`, `refreshed_at`, `captured_at`, `source_observed_at`, `retained`, `ambiguous`, `conflicting`, `identity_state`, `text_truncated`.

### people show

`m365crawl people show person-observation:ID` follows a native list hit. It returns one list item with its original capture identity, all retained differing group representations, same-source email/directory conflicts, historical/current profile-owner link evidence and `identity_state: "unresolved"`. Neither native versions nor missing conflicting copies establish a current winner. Existing Teams and `mail:<address>` IDs retain their original workflows, not this inspection command.

`--fields` keys: `id`, `display_name`, `email`, `sources`, `source_id`, `capture_id`, `native_class`, `native_key`, `native_parent`, `version_raw`, `resynced`, `first_name`, `last_name`, `alternate_email`, `teams_id`, `refreshed_at`, `captured_at`, `source_observed_at`, `retained`, `ambiguous`, `conflicting`, `identity_state`, `text_truncated`, `copies`, `conflicts`, `captured_profile_link`, `current_profile_link`, `explanation`.

### people sources

`m365crawl people sources` reports registered native people inputs and their independent attempts, admissions, failures and coverage. Coverage remains outside selected item fields and visible at zero hits. `partial` is useful evidence, not a complete directory; no physical occurrence count is inferred from distinct display representations.

`source_id` is null until a capture of the locator is admitted; `kind` and `locator_tail` identify the row.

`--fields` keys: `kind`, `source_id`, `locator_id`, `attempt_id`, `admitted_id`, `platform`, `locator`, `boundary`, `status`, `coverage`, `qualification`, `acquisition_signature`, `content_digest`, `reason`, `attempted_at`, `admitted_at`, `complete_at`, `source_observed_at`, `losses`, `locator_tail`, `fix`, `effect`.

### people sync

`m365crawl people sync` registers supported Mac HxStore profiles. `m365crawl people sync --root DIR` registers supported copied `<root>/<profile>/HxStore.hxd` sources on supported hosts. Windows OWA people discovery is not implemented. Automatic reads use registered locators only and respect the five-minute HxStore copy minimum interval; `--max-age 0` neither captures nor creates/repairs an archive. Explicit sync retries. A normal Outlook capture may reuse its already-required copy for registered eligible people without changing healthy calendar/mail covering status.

Partial and failed capture retains native representations and durable contradiction evidence. A cache missing a person is not deletion. Native identity strings outside the qualified syntax remain raw evidence; no email-to-person resolution or subject tenancy is inferred. `people sync/show/sources` reject subject `--account` filtering.

Symlink and special-file source leaves are refused explicitly (`outlook_people_source_unsafe`); use a supported regular copied HxStore file. Unsupported default provider errors (`outlook_people_source_unsupported`) point to `--root`, not archive repair. Reader panics remain failed optional captures with cleanup verification. List coverage is refreshed after same-command legacy/shared capture; native ordering uses full names and numeric IDs before output text truncation.

Use a Teams person's `id` or name with `--from` on Teams commands, and a mail address with `--from` on `mail list`.

```sh
m365crawl people --query alex
m365crawl people --query @example.com
```

## sql

Run one read-only SQL query against the archive.

```
m365crawl sql <query> [flags]
```

| Argument or flag | Meaning |
| --- | --- |
| `<query>` | One SELECT (or WITH, EXPLAIN or VALUES) statement. |
| `--limit=50` | Maximum rows to return; the query stops there and `truncated` says whether more rows exist. |

Result: `{"columns", "rows", "count", "truncated"}`. It stops reading at `--limit`, so there is no `total`. BLOB values (such as `messages.raw_digest`) are printed as lowercase hex in every format. Returns every account's rows regardless of `--account`. The mail tables are `mail_messages`, `mail_folders`, `mail_recipients`, `mail_attachments` and `mail_coverage`.

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

Application evidence. Every Teams message item carries `app_observation`, the app, reply and citation claims of its archived native JSON, and the result carries `application_evidence_coverage`. The claims are the message's own, not a verified app identity or citation target, and one message stays one item. `messages`, `unread`, `thread`, chat hits of `search` and the recent messages of `calendar event` share this shape.

- `app_observation.state` is `mapped` (claims read), `partial` (some read; `served.losses` counts what was not), `absent` (no app or citation claims), `failed` (the native JSON could not be read; `failure_code` says why, for example `teams_app_projection_corrupt`) or `not_derived` (not projected yet; run a writable `m365crawl sync`, because `--max-age 0` never derives it).
- `served` holds the claims: `app_id`, `app_name`, `reply_to_id` and `citations` (`position`, `id` as a decimal string, `title`, `link`, `content_type`, `nested_content_present`). At most 100 citations are previewed; `total_admitted` and `citations_truncated` say how many exist. `full_inspection` is an archive-only `m365crawl --db PATH --max-age 0 sql` command that prints the complete stored representation. It names the same `--db` archive the read used, as an absolute path (double-quoted on Windows, POSIX single-quoted elsewhere, when it needs quoting), so run it as printed.
- When the current representation failed, `served` is the last good one, with `retained: true` and `retained_reason: "current_failed"`. `current` describes the message as archived now (`sender_id`, `raw_present`, `raw_digest`, `mapper_version`).
- `application_evidence_coverage` counts the states (`mapped`, `partial`, `absent`, `failed`, `not_derived`) and `retained` of the Teams messages actually returned, after `--limit`. With no Teams message returned, its `state` is `unknown`, not absent. It is not app, service or Copilot completeness.
- Text output marks an app message `[app: NAME]` in the text column, lists its citations under the table and ends with an `app evidence of N returned Teams messages` line.

```sh
m365crawl messages -c "Fixture team 1 › General" --since 2023-11-14 --max-text 80
m365crawl messages --unread --include-channels --limit 5 --fields id,sender_name,text
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `app_observation`.

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

Result: a list of message items, newest first, with `app_observation` and `application_evidence_coverage` as in `messages`; with `--by-conversation`, one overview item per conversation and no application evidence. Results carry `channels_excluded: true` when channels are left out.

```sh
m365crawl unread --limit 5
m365crawl unread --by-conversation --since 7d
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `app_observation`. With `--by-conversation`: `conversation_id`, `conversation_display_name`, `kind`, `unread_count`, `oldest_unread_at`, `newest_unread_at`, `link`.

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

Result: a list of message items, root first, with `app_observation` and `application_evidence_coverage` as in `messages`. The thread is read in one archive snapshot.

```sh
m365crawl thread 19:topicchannel1@thread.tacv2 1700000045000
m365crawl thread "https://teams.microsoft.com/l/message/19:topicchannel1@thread.tacv2/1700000046000?parentMessageId=1700000045000"
```

`--fields` keys: `tenant_id`, `user_id`, `conversation_id`, `conversation_display_name`, `id`, `reply_chain_id`, `parent_message_id`, `sender_id`, `sender_name`, `sent_at`, `edited_at`, `deleted_at`, `message_type`, `text`, `text_truncated`, `html`, `mentions`, `mentions_me`, `mention_kind`, `reactions`, `files`, `links`, `subject`, `importance`, `pinned`, `link`, `reply_count`, `last_reply_at`, `app_observation`.

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

The `mail` commands read new Outlook cached mail from the archive: HxStore on Mac and OWA IndexedDB on Windows; explicit supported OWA copies work portably. The archive holds only cached mail plus what earlier syncs kept. Every result says when mail was last synced and how far back the cache reaches.

- **Ids.** A message id is `<account>:<detail_key>`, split on the last colon, as `mail list` prints it (for example `outlook/Main:12345`). `--account` takes the mail account as it appears in the ids (`outlook/<profile>`), not a Teams account.
- **List results** are `{"items": [...], "count", "truncated", "synced_at", "coverage": [{"account", "folder", "kind", "oldest_at", "newest_at", "count"}], "note"}`. `note` explains an empty result (no archive yet, Outlook source off, no Outlook profile, mail not read yet, filters matched nothing, or the cache covers only since a date). For unread results it says how many messages the cache holds in each folder, for example "the local cache holds 17 messages in Inbox; Outlook may show more", or "no unread mail in the cache; Outlook may show more". Text output of every `mail` list ends with `synced <time> · cache covers since <date>`.
- **Limits.** `--limit` is at most 1000 on every `mail` command; a larger value is a `usage` error.
- **Missing folders.** A message whose folder Outlook's store no longer holds is kept, with `folder` null and `folder_kind` `unknown`. `--folder unknown` lists those messages.
- **Not known is null.** A field the archive does not hold is `null`, never guessed: `subject`, `from_name`, `from_address`, `in_reply_to`, `ical_uid` and `internet_message_id` when Outlook stored none; `is_read` when the read state is not known; `sent_at` when the store has no send time; `address` on a recipient without one; and `gone_at` and `evicted_at` while the message is present.
- **Recipients.** Mac To and Cc are not yet told apart. Every recipient carries `name`, `address`, nullable `role` and `kind_raw`. Mac role is null and its existing raw kind remains; Windows preserves ordered To/Cc/Bcc roles with `kind_raw=0`, without ReplyTo inference.
- **Importance** (`low`, `normal`, `high`) is likely right, but it is read from one store field that is not fully confirmed.
- **Gone and evicted.** A message a sync saw disappear inside the cache's covered range is `gone`; one older than the covered range is `evicted`, because Outlook dropped it from the cache. Both stay in the archive and are hidden unless you ask for them.
- **On Windows** native OWA cached mail is supported. `conversation_id` is nullable on every message output, including search and a calendar event's `related_mail`; thread grouping tries conversation identity first. Windows limitations are described in [source differences](outlook-store.md#windows-owa-cached-mail).

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

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `conversation_id`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

### mail show

Show one message: headers, recipients, attachments and body text.

```
m365crawl mail show <id>
```

Keys are those of `mail list`, with `recipients` (every recipient, each `{name, address, role, kind_raw}`) in place of the count and preview, plus `read_state`, `attachments` (each `{name, size, content_type, inline, downloaded}`), `body_text` and `body_state`. Windows attachments are metadata only (`downloaded=false`, missing size zero); unknown HasAttachments is not inferred from metadata. Cached HTML may already be truncated inline at source. `--max-text N` cuts `body_text` and sets `text_truncated`. A malformed id is `bad_mail_id`; an id the archive does not hold is `mail_not_found`; an `--account` that differs from the id's account is `account_mismatch` (the id already names the account, so `--account` is not needed).

```sh
m365crawl mail show outlook/Main:12345 --max-text 2000
```

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipients`, `in_reply_to`, `conversation_id`, `received_at`, `sent_at`, `is_read`, `read_state`, `flag`, `importance`, `has_attachments`, `attachments`, `preview`, `body_text`, `body_state`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

### mail thread

Show a message's conversation: shared conversation identity first, then reply chain, then subject and shared participant.

```
m365crawl mail thread <id>
```

Items have the shape of `mail list`, oldest first. `grouping` first tries `conversation` (at least two messages sharing an exact nonempty account-scoped `conversation_id`), otherwise `reply_chain` (In-Reply-To and Message-ID links followed in both directions), otherwise `subject` (the same subject, ignoring `Re:`, `Fw:` and similar prefixes, with at least one shared participant). `participants` lists each person in the thread once (`name`, `address`). When no other message joins it, the thread is the message alone and `note` says so. `mail thread` takes the same ids as `mail show` and returns the same id errors.

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `conversation_id`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

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

`--fields` keys: `id`, `account`, `folder`, `folder_kind`, `to_me`, `subject`, `from_name`, `from_address`, `recipient_count`, `recipients_preview`, `in_reply_to`, `conversation_id`, `received_at`, `sent_at`, `is_read`, `flag`, `importance`, `has_attachments`, `preview`, `internet_message_id`, `ical_uid`, `gone_at`, `evicted_at`, `text_truncated`.

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
- `chat`: the Teams meeting chat (`conversation_id`, `display_name`, `message_count`) and `recent_messages`, its newest 20 live messages, newest first, each in the `messages` item shape with its `app_observation`; the event's `application_evidence_coverage` counts those messages. `chat` is null when the archive holds no meeting chat for the event. An event with no Teams meeting chat at all, such as one only Outlook holds, also gets a `notices` entry saying so.
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

In text mode each seam is one line, such as `── part 2 of 3 · 10:02 PST · fetched 2026-11-09 08:00 PST ──`, `── part 3 of 3 · 10:40 PST · not fetched yet; run m365crawl transcripts fetch <call-id> ──` or, after a failed attempt, `── part 2 of 3 · 10:02 PST · not fetched: <reason> ──`; consecutive lines of one speaker are joined, and the last line says how many parts are fetched.

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

Ask the user before running it: it opens a visible Edge window where they sign in to Microsoft 365 once. Run it only after they say yes. If the window asks to enroll or register this device, the organization signs in only from managed devices: tell the user, do not run it again, and keep using the commands that read the archive offline. When stdin is not a terminal (an agent runs it) it refuses without `--user-agreed` (`signin_needs_agreement`, exit 2); on a terminal it says what will open and waits for Enter. The window opens on a site of the host, the site of the first transcript part the archive holds there (as `transcripts fetch` opens it), because the bare root of a OneDrive host (`<tenant>-my.sharepoint.com`) sends the window off the host; with no such part it opens the host's root. It prints each step on stderr (opening the window, waiting for sign-in, signed in) and waits up to 5 minutes until the window is signed in: on the SharePoint host, past its sign-in pages, and with SharePoint's API answering as for a signed-in user (the site's own, or else the host's root site, so a site the user cannot open, such as another person's OneDrive, still counts once they are signed in). Then it closes the browser. The window uses m365crawl's own profile next to the archive, never the everyday browser profile.

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
