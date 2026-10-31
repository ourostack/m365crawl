# Changelog

All notable changes to m365crawl are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/) with the 0.x caveat that the JSON contract in [SPEC.md](SPEC.md) is stable except where an entry below says "Breaking".

## [Unreleased]

## [1.4.0] - 2026-10-31

### Added

- Mail message outputs add `older_copy` and `copy_of`; `coverage[]`, `mail folders` items and `mail unread` folders add `current`, which is `false` for a folder of an older folder set.
- `mail list --since/--until` adds `folder_copies`, which compares the current copy of Inbox and Sent Items with their older copies over the range and reports `partial` when an older copy holds messages dated inside the current cache's range that the current set does not hold in any folder, with a notice. `older_copies_marked` counts older-copy messages the list shows only with `--include-gone --include-evicted`, and the notice names those flags.
- Windows new Outlook profiles with several content mailboxes now read each mailbox into its own account (`outlook/<profile>/mailbox-2`, `-3`, …), attributed from the cache's own mailbox identity rather than the database name. The profile's first mailbox keeps `outlook/<profile>` and its public IDs; mailboxes that cannot be attributed still refuse with `outlook_mail_layout_unsupported` and apply nothing.
- `--no-sync` (also `M365CRAWL_NO_SYNC=1`) reads the archive as it is: a read never syncs first, whatever `--max-age` says. The flag, including `--no-sync=false`, and an explicit `--max-age` beat the variable. Explicit `sync`, `fetch`, `capture` and `signin` commands are unaffected.
- Every sync-backed read result carries `implicit_sync`: `ran`, `skipped_fresh` (the archive was younger than `--max-age`) or `disabled` (`--no-sync` or `--max-age 0`), beside `archive_age_seconds`, alongside the lock-wait values `waited_for_other_sync` and `other_sync_in_progress`. Notes, docs, Loop and native people reads say `implicit_sync: "disabled"` when `--no-sync` or a zero max age skipped their registered-source refresh, with a notice for a zero max age, or for `--no-sync` once their oldest registered source was last read more than 15m ago.
- Opt-in Microsoft Scout session history: `scout sync` copies the session files Scout keeps in your local OneDrive folder into the archive (macOS; Linux with `--root`), and `scout list`, `scout search`, `scout show` and `scout sources` read them offline. Only files already on this computer are read: a cloud-only file is counted in `scout_coverage` and never downloaded. Each version of a session is kept as an immutable observation with its digest, sessions Scout later deletes stay in the archive (`file_state` gone), and secrets are stored as `[redacted]` under the Teams scrub rules plus Scout rules for tool output (Bearer, GitHub, `sk-`, Slack, AWS and npm tokens, private keys, connection-string and URL secrets, `*_TOKEN`/`*_SECRET`/`*_PASSWORD`/`*_API_KEY` assignments, Cookie headers, and values under JSON keys that name a secret). When those rules or the session reader change, the next `sync` or `scout sync` scrubs and maps every archived session again in place from the archive alone, including sessions no longer in OneDrive, and overwrites the replaced text; `sync` reports `scout_rescrubbed`. Each session is re-scrubbed in its own commit, so a full disk or crash stops nothing else: an ordinary sync reports `scout_rescrub_failed` and still syncs Teams and Outlook, and the next sync resumes. A session that no longer maps after scrubbing is blanked and counted rather than kept. The archive schema is now 21; the new tables are additive.
- `scout sync` now reads Microsoft Scout session history on Windows, from the `Apps` folder of each OneDrive folder named by the `OneDriveCommercial`, `OneDriveConsumer` and `OneDrive` environment variables, or a folder named with `--root`, on a local fixed NTFS drive. As on macOS it never downloads anything: a file whose folder listing shows OneDrive keeps it only in the cloud (offline, recall-on-open, recall-on-data-access or partly downloaded) is counted as `cloud_only` and never opened; other files are opened without following links and asking Windows not to download, and their attributes are checked again before and after the read.
- Mail message outputs add `dated_at` (the time a message is listed, filtered and sorted by) and `received_at_disagrees` (copies in other folder sets hold received times more than a minute apart); `folder_copies` entries add `dated_by`. Text mail tables show `dated_at` in a `date` column.

### Changed

- Emitted follow-up reads (`full_inspection`, `full_text_sql`/`full_text_command`, native attendee routes, retries that read) pin `--no-sync` instead of `--max-age 0`; emitted explicit sync, fetch, capture and signin commands carry no freshness flag. Help, the agent guide (`m365crawl skill`) and the docs teach `--no-sync`. Migration: use `--no-sync` where you meant "do not sync"; for fresh data run `m365crawl sync` first or keep a positive `--max-age`.
- The help, agent guide, scope page and SPEC now say why private Copilot chat history is not included: Microsoft's export of Copilot interactions supports only an organization-wide application permission that an administrator must approve, not a signed-in user's own access.
- Writable archives migrate to schema 22 (after the Scout tables of schema 21) for optional, disabled-by-default WorkIQ source state. Older read-only archives are not migrated; older binaries are not promised to read the new schema.
- Apply schema creation atomically, avoiding a separate commit per DDL statement and partial table creation when setup fails.
- Check before every WorkIQ mail page that the archive's disk can hold the rest of a whole-account sync, including admitting every fetched item, and stop with `workiq_disk_space` (position kept) and the estimate otherwise. The whole folder tree is listed before the first message page, so the check refuses before any message is fetched; a catch-up after a complete sync needs only headroom. Routes state the measured cost, or say whole-account sync is refused on that archive and how much space to free.
- Reopen a WorkIQ process that ends mid-run and retry the same page; when it keeps ending, stop with resumable `workiq_retry_later`. Failed runs report an allowlisted `diagnostic` (exit status and output tails reduced to common log words, error class names, status numbers and hashed request IDs, never message content), kept as source health's `last_diagnostic`; a failed server, tool or JSON-RPC result adds `server` (HTTP status, Graph error code from WorkIQ's nested error object or JSON-RPC code, and the allowlisted shape of its text, at most 256 bytes); other numbers, such as counts and phone numbers, are redacted.
- Keep one entry per chat when Graph's chat listing repeats a chat (`counts.chats_repeated`), instead of failing discovery. Reopen a WorkIQ process lost during the account identity check, and retry an unexplained WorkIQ tool error mid-run like a server error, so both end as resumable `workiq_retry_later`; when the next run stops at the same position for the same reason, it reports `workiq_stuck` (not resumable) instead of inviting endless reruns. Every failure, including one in chat discovery or the identity check, records its attempt, result entry and diagnostic.
- Scope WorkIQ run ownership to source, account and kind, so runs of different accounts or sources proceed together and a stale attempt is closed only by the next run of its own scope; count only pages a chat sync fetched against its page limit, so large chats continue across runs (`resumable: true`); report the restarted run's own code after an expired-delta restart.
- Give archive writes a budget sized to the page with bounded retries (`workiq_archive_busy`, resumable), store the same failure code the command prints, report exhausted transient failures as resumable `workiq_retry_later`, count Graph's repeated chat messages once, close attempts left open by an unsaved outcome as `workiq_interrupted`, let `workiq status` without `--account` name each Outlook account to choose, and keep WorkIQ runs out of `last_run` and doctor's `last_sync_status`.
- Read WorkIQ 1.0.1's `1.0.1.47154` version output; report a silent signed-out WorkIQ as `workiq_signin_required` after a 30-second initialization deadline instead of a two-minute timeout; resume large mailboxes across runs at the page limit instead of recounting earlier pages, marking only that stop `resumable: true`, and continue an owed removal recheck across runs instead of restarting it each run; explain stdout log noise as a protocol error.
- Follow WorkIQ mail next and delta links by rewriting only their `/me/mailFolders('<id>')` segment to `/me/mailFolders/<id>`, which WorkIQ requires; wait for a 429's `retryAfterSeconds`; and report WorkIQ's path refusal as `workiq_policy_denied` without its echoed path. These shapes were observed through WorkIQ's MCP `fetch` connector and confirmed over the installed `workiq mcp` 1.0.1 executable.
- Route agents to WorkIQ: a mail read whose `--since` is before a read folder's cached mail (each folder judged on its own), a thread reply whose parent is not archived, or a chat read before the oldest archived message carries `workiq_routes` with the exact `workiq status`, `enable`, `sync` or `coverage` commands to run next, without starting WorkIQ. Agents may run them on their own; WorkIQ's install, sign-in and EULA stay one-time human steps.
- Add explicit WorkIQ enable/disable, all-folder mail sync, selected-chat counts, coverage and initial-path status probes through the user's subprocess. Ordinary reads/sync/watch never launch WorkIQ; enabled doctor runs version only. No login, consent or agreement automation.
- Preserve canonical IDs and per-source evidence, report provenance on message/conversation/mail reads, and keep server content through desktop rebuilds. Unqualified message-ID/clock classes remain conservative gaps rather than duplicate chat rows or unsupported resurrection.
- Distinguish original observations from replay completion when reporting server-covered mail health; both must be under 24 hours old, and the original local failure stays visible.
- Count each WorkIQ gap once in `workiq_gaps`, say when an account narrowed `--source all` to one family (`notices`), and let a WorkIQ mail sync that covered every folder clear a stale Outlook mail cache warning.
- Never retire a WorkIQ health entry as a missing Outlook profile, and report an unreadable WorkIQ outcome row as that source's failure (`workiq_outcome_invalid`) instead of failing every read. Bind a Windows mailbox account (`outlook/<profile>/mailbox-N`) only through its sealed mailbox identity, never by bootstrap.

### Fixed

- Reads run in parallel no longer fail with `locked`. When a read's implicit sync finds another m365crawl run holding the archive lock, it waits for that run (up to 2 minutes) and answers from its sync without syncing again (`implicit_sync: "waited_for_other_sync"`); if the wait runs out it answers from the archive as it is, exits 0 and says so (`implicit_sync: "other_sync_in_progress"` and a notice) instead of reporting `sync_error: locked`. Notes, documents, Loop and people refreshes inside a read wait the same way. Explicit `sync` still exits 4 `locked`.
- Mail on Mac no longer lists or counts a message twice when Outlook has rebuilt the account with a new set of folders: a message from an older folder set that the current set holds in any folder (same Message-ID; Sent Items matches only Sent Items), or a newer older set holds in the same kind of folder, is listed and counted once, from the newer copy, in `mail list`, `mail unread`, mail `search`, `mail thread`, the `mail folders` counts, the status mail block and a calendar event's `related_mail`. Nothing is deleted and ids do not change; `mail list --include-copies` lists the older copies too.
- A trusted mail read no longer clears the coverage of a folder from an older folder set it does not read, and the gone/evicted rule judges against the read's own folder set only.
- The empty-result note and the "cache covers since" footer of the mail lists measure from the current folder copies, and the note says when only an older copy of the folder reaches the requested date.
- A `--no-sync` read of notes, documents, Loop or native people sources prints the same caution every time for the same archive: it now names when the oldest source was last read (an absolute UTC time) instead of how long ago, which changed from one minute to the next (#283).
- A read that skipped its implicit sync no longer looks fresh. `--max-age 0` keeps its 1.x meaning (no implicit sync), but its results now say `implicit_sync: "disabled"` and add a notice that it turned the sync off rather than asking for fresh data; `--no-sync` adds the notice when the archive is older than 15m. Text mode also prints a `hint:` line on stderr (#264).
- A calendar event's `related_mail` dates mail as `mail list` does: a reply in Sent Items is matched to the 14-days-before to 7-days-after window, and ordered, by when it was sent, not by the received time Outlook stores on it (often when the draft was started). Each `related_mail` item adds `dated_at`, and text output shows it in a `date` column.
- `mail list --since/--until`, mail `search` and the folder-copy check date Sent Items by when the message was sent (`sent_at`), not by the received time Outlook stores on it, which is often when the draft was started or a time on the next day: a message sent late in the evening now lists on the day it was sent. Other folders, Drafts included, stay on the received time. Lists sort newest first on the same clock, and `mail thread` oldest first, so a sent reply whose stored received time is the draft's start no longer appears above the messages it answers.
- When copies of a message in different Outlook folder sets disagree on received time by more than a minute, the message is dated by the earliest copy's received time at or after its send time (15 minutes of clock skew allowed), instead of by whichever copy the folder-set ranking picked; with no such copy, the listed row's own time stays.
- A closed Outlook no longer reads as healthy. Mail now records when Outlook last wrote its local cache (`cache_fresh_at`, the clock the Outlook calendar already keeps) and shows it on `mail list`, `mail folders`, `status` and the overview. When a sync finds that Outlook has not written its cache for more than an hour, mail and calendar reads that reach past that time carry a `source_health` entry with `state: "stale"`, `last_error_code: "outlook_cache_stale"` and `cache_fresh_at`, plus a notice that mail (or calendar changes) after that time are not in the cache and that Outlook must be opened before `m365crawl sync`. `doctor`'s `mail_readable` and `outlook_store` checks warn with the same fix. Stale entries are not failures: sync reports' `failed_source_health` does not list them, and statuses and exits are unchanged.
- `--until` now means the same thing on every date-bounded read (`messages`, `search` for every source, `mail list`, `transcripts`, `notes list`/`search`, `docs list`/`search`, and `--modified-until` on `content list`/`search`): every read selects the half-open window from `--since` up to, but not including, `--until`. A date alone as `--until` includes that whole local day, so `messages --since D --until D` returns the messages of day D instead of nothing. A time as `--until` is excluded, so a message stamped exactly at midnight is counted in one day, not two. Before, `messages`, chat search and `content` read a date alone as the start of that day and included a time, and mail, transcript and notes search and `notes list`/`search` read a date alone as the start of that day; `mail list`, `transcripts` and `docs` already behaved this way (#257).
- A read whose `--until` ends at or before its `--since` now adds a `notices` entry saying the time window is empty, instead of only noting that nothing matched (#257).
- `docs/commands.md` has a Time windows section that sets `--since`/`--until` beside `calendar --from`/`--to`, whose `--to D` still ends at the start of day D (#257).

### Security

- A record archived under a key that held a credential no longer keeps that key after the purge. The row takes the scrubbed key, or, when another row already holds the scrubbed key, it is deleted (the one row the archive deletes). This also covers rows an earlier version cleared but left under the old key, the calendar losses kept under it, and Loop registrations, which keep the record key as their identity: a registration takes the scrubbed key, or is retired with everything it owns when another registration already has it (#276).
- The writable archive now always runs with SQLite's `secure_delete` on, so anything a sync replaces, clears or deletes is overwritten with zeros instead of being left in free space. On a copy of a real 2.9 GB archive this did not measurably change sync CPU time. An archive an older version wrote is rewritten once (`VACUUM`) by the next `sync` or `watch` when its disk has twice the archive's size free, so nothing that version left in free space remains; otherwise the sync report and `doctor` say `vacuum_pending` with the space needed, and a later sync tries again. A read's implicit sync never runs it (#276).
- The Teams credential purge (rows under a name the denylist now matches, or under a key that now scrubs differently) no longer leaves the cleared values on disk. It runs before the sync's removal marks, with SQLite's `secure_delete` on, so the values are overwritten with zeros, and the sync then truncates the archive's write-ahead log. When another program reading the archive blocks that, the sync report sets `purged_log_pending` and the next sync tries again. Copies that older syncs left in free space are overwritten only when SQLite reuses the space (#275).
- Redact whole Graph download URL values and temporary authentication URL parameters, including escaped JSON. The new scrub signature causes one full reread of unchanged desktop-cache records after upgrading.

## [1.3.0] - 2026-10-28

### Added

- `content fetch engage-thread URL` captures an exact Engage thread or Teams Engage deep link: observed starter/replies, native authors/times, safe links and generic attachment metadata, with explicit partial/loss reporting, immutable archive evidence and offline content show/search. Unsafe signed or credential-bearing URLs are omitted/redacted; attachments are not downloaded or classified into file/image types.
- Selected-thread capture can attempt a fresh exact-target landing after a positively committed same-frame document replacement, without weakening generic navigation or final native identity/generation checks. Landing, collection and transport limits remain unchanged.

### Fixed

- Tool-owned browser navigation waits for the requested document's committed loader and complete readiness instead of accepting a completed previous document. Same-document navigation retains its bound loader; missing identity, downloads and superseding documents fail closed without changing acquisition deadlines or Engage evidence checks.

## [1.2.0] - 2026-10-27

### Added

- Archive reads now disclose failed in-scope Teams, Outlook mail and Outlook calendar sources through `source_health` and notices, including zero-hit searches and every mailbox read by calendar event. Sync reports expose participating source/account failures through `failed_source_health`. Schema 20 retains source refusal codes/account scope in the existing sync outcome history and tracks actual mail modifications without changing `state_seen_at`. Legacy read-only history remains visible. Failed outcome persistence returns a database error without claiming fresh success. Legacy account-wide failures remain for accounts not read by a scoped recovery. Implicit reads cannot claim checked-empty health after failed outcome persistence, and cancelled reads retain database diagnostics in their existing error message. Existing source statuses, streams and normal exits are unchanged.

### Fixed

- Homebrew release verification checks the published cask version without loading
  its Ruby before the fully qualified install. Homebrew's trust defaults and the
  installed-version and signature checks remain unchanged.

## [1.1.1] - 2026-10-26

### Fixed

- Read the macOS Outlook event layout `0x6b/0x459` and mail-body layout
  `0xca/0x752` alongside the older supported layouts, restoring mail and calendar
  reads after the October Outlook update. Unknown layouts remain explicit.
- Every mail read and mail-participating search carries a retained mail-read
  failure as a stale-data notice, even offline, with no matching items or with
  selected fields. A successful mail read clears the notice.
- Unknown Outlook account layouts preserve previously established calendar
  identity and automatic links rather than clearing them as confirmed empty.
- Offline mail search remains compatible with older readable archives without
  retained failure storage.

## [1.1.0] - 2026-10-23

### Added

- Read cached new Outlook OWA calendar events on Windows through the supported calendar queries, source counts and doctor paths, alongside existing cached mail.
- Windows OWA doctor reports independent cached calendar and mail read health, including calendar failures without treating committed mail as unread. Calendar queries and source counts retain an unlinked profile when sign-in identity is unverified.

### Fixed

- Retain explicit unknown attendee responses, render decoded unpaired UTF-16 Preview units at the text boundary, and preserve unresolved timezone labels with their known UTC intervals without counting retained uncertainty alone as an omitted event. Malformed fields, unsupported values, partial attendees and unproved all-day dates still report loss.
- Update installation, privacy and embedded agent guidance to describe the supported Windows OWA calendar without broadening separate native people/attendee or multi-mailbox support.
- OWA calendar sources report that deletions are not inferred and omit inapplicable HxStore block-health ratios. Last-good census provenance survives later failures; older development receipts requalify calendar without rereading unchanged mail.

## [1.0.2] - 2026-10-21

### Fixed

- Initial release rehearsals leave a bounded observation window before the tap push so controlled failure tests need not race runner startup. Stable publication and publish-only recovery are unchanged.
- Partition the complete macOS release race-test suite using the existing CI test-name sharder, preserving every check, race option and timeout budget while requiring every partition to pass before publishing.
- Require Go 1.27.3 or newer so builds include its `crypto/x509` security fixes.
- Preserve supplied release notes and signing disclosures in published releases,
  and refuse promotion when the published body does not match. The stable release
  badge and installation/agent guidance now explicitly identify the stable 1.x
  contract without broadening native source coverage.
- Wait for the owned file-event pump and its watcher-close call before returning
  from watch shutdown, rather than letting that cleanup continue after return.
- Retain a Windows Outlook mail snapshot when an aborted open or failed reader close cannot prove that its lazy table reader was released. Preserve the original failure or cancellation and report the retained snapshot without exposing cached content; healthy and proved-canceled reads still remove their snapshot. Calendar support and mailbox attribution are unchanged.

## [1.0.1] - 2026-10-20

### Fixed

- Incomplete Teams IndexedDB observations retain readable data without treating hidden keys or database names as removals, marking retained calendar events gone, or advancing calendar verification. Truncation stays visible, including an empty partial census; unchanged-cache fingerprints and read memory are invalidated for the corrected decoder.
- A first incomplete calendar read now exposes retained events and normal exact-address Outlook links immediately, while replay remains pending. Archive-only recovery preserves existing sync and cache-fresh clocks and grants no new verified days or coverage window.
- Cancellation after a committed Outlook calendar keeps that committed result while reporting interruption instead of refreshing covering success. Unread mail remains eligible for the next ordinary sync.
- An unchanged Teams cache with unresolved report-significant omissions is read again so diagnostics cannot silently disappear. Restored content clears the loss after a real read; clean denylist/redaction-only observations retain their unchanged shortcut.
- Read the measured extended Outlook for Mac mail-folder layout as well as the original layout, restoring folder names, kinds and message links. Existing archives re-read unchanged stores under the new mail mapper; unknown layouts still report an explicit partial-read loss.
- The portable OWA acceptance body oracle decodes SQL's hexadecimal BLOB representation before its bounded gzip comparison. Strict null, type, malformed hex/gzip, body and FTS corruption checks remain; production storage and native collector qualification are unchanged.
- Loop retries and compact inspections, Office registry/OneDrive usage retries, Outlook link fixes and event transcript follow-ups retain the selected archive and output format. Source/link recovery also retains applicable roots, profile/principal and account context without copying incompatible item flags.
- Teams and Outlook snapshot roots record protected ownership before source copying. Stale sweeping requires both age and positive owner-death evidence and rechecks root identity; active, reused-PID, uncertain, unprotected and legacy roots are retained so another archive's lazy reader keeps its backing files. Registration failure cleans the new root and fails capture. Windows process-open failures never imply death, so unprovable abandoned roots remain.

## [1.0.0] - 2026-10-17

### Fixed

- Clarify the stable-major public compatibility contract, directional archive schema support, supported per-field text clipping routes and mandatory macOS release signing without changing runtime behavior.

## [0.9.17] - 2026-10-17

### Added

- Inspect strictly qualified own Teams cached regular-compose bodies with
  archive-only `drafts list/show`, independent coverage and body-frame-only guarded
  follow-ups. These are retained projections, not current-unsent/editor truth;
  target metadata is freshly read outside the body guard. No new acquisition,
  shared-search inclusion or message action. Native evidence is one Mac
  Space/team-side body plus historical removed Chat shapes, not current chat,
  lifecycle, native Windows or all-drafts qualification.
  Windows emitted follow-ups explicitly require PowerShell7.3+ Standard argument
  passing (not CMD) and preserve literal expansion characters and smart quotes;
  missing selected archives retain generic unknown with archive-missing context.

## [0.9.16] - 2026-10-16

### Fixed

- Native attendee reads now explain explicit account filtering and archive-wide source diagnostics without changing owner qualification or read selection.
- Root help and the embedded guide explicitly distinguish cached bot/recap evidence from deferred private Copilot history and Planner/Microsoft To Do assigned-task ingestion.

## [0.9.15] - 2026-10-16

### Added

- Native Outlook attendee evidence through explicit `calendar attendees sync` and
  strictly offline `sources`, `list` and `show`: immutable physical rows, separate
  selected-detail bindings, source-local ambiguity, original losses/clocks and
  selected-archive full-record/capture routes. Native observations remain separate
  from the retained/unioned displayed roster and never establish current membership
  or resolved identity. Mac default HxStore and supported copied profiles are
  scoped separately from unqualified native Windows/OWA acquisition.
- One additive schema19 with unique successful ordinary calendar read receipts,
  protected link frontiers and immutable per-attempt native evidence membership.

### Changed

- Real-cache Outlook acceptance reports actual cache size, timing and memory use instead of enforcing assumed performance cutoffs. Capture, sync, measurement-protocol and source-safety failures remain blocking, and unavailable metrics remain explicit. Production resource bounds are unchanged.

### Fixed

- Calendar/native/people readers share one private-copy lifecycle and close before
  verified cleanup. Optional failures remain lane-local; cleanup/cancellation
  prevents optional positive admission.
- Standalone calendar/native capture invalidates the mail-read marker before the
  shared fingerprint advances, preserving pending mail for the next eligible
  ordinary unchanged-input sync without reading or erasing mail itself.
- Existing unknown calendar origins cannot be claimed by standalone capture:
  refusal precedes calendar/mail/native changes, with exact ordinary-bootstrap
  recovery after source verification.
- Historical observation/binding previews retain bounded own-snapshot capture
  routes independently of current source health and field projection.
- Source-ID unknown-origin recovery retains a separately audited admitted
  original root across source-ID-only retries, without combining selectors,
  rewriting history or guessing a missing bootstrap root.

## [0.9.14] - 2026-10-15

### Fixed

- `teams` and `--team` now recognize archived `Space` roots without cached child channels. Team-filtered reads include root messages, and a zero channel count describes the cache, not the service.

## [0.9.13] - 2026-10-14

### Fixed

- `--max-text N` now includes the ellipsis within each supported displayed field's
  N-Unicode-code-point bound, including Engage block/title previews. N=1 clipping
  emits only the marker; exact-bound values and0/unbounded behavior remain unchanged.
  Original archived evidence, occurrence counts, native IDs and full-record
  recovery are unchanged. Help now describes applicable read/detail surfaces and
  the per-field bound accurately.

## [0.9.12] - 2026-10-14

### Fixed

- Browser commands preserve the caller's cancellation or deadline error when the browser connection has already closed, rather than reporting a disconnected browser.
- Transcript fetches recognize Microsoft dogfood SharePoint tenant hosts under
  `.sharepoint-df.com`, including personal sites, without weakening host-label,
  site-root, identifier or reference-quality checks. Unsupported hosts now have
  a distinct reason instead of being reported as malformed identifiers.

## [0.9.11] - 2026-10-14

### Added

- Explicit `content fetch engage-home` archives bounded native Engage access-session
  thread-starter observations with immutable sessions, ordered duplicate occurrences
  and portable offline search/detail/full-record SQL. Additive schema18 keeps
  registration-level unknown failures separate from verified native account instances.
- Exact-root, user-agreed Engage sign-in verifies positive native session evidence
  only after browser Close and discards content. Preview100, original-session losses,
  native clocks and independent source coverage are discoverable in CLI help.
- Scope remains partial native observations, not a complete feed, replies, mentions,
  targeted-thread retrieval or current revisions. Live Windows/Linux service acquisition
  and interactive challenge/expiry are not qualified by synthetic fixtures.

### Fixed

- Mixed content and source text output now uses correctly labeled consecutive
  tables instead of mixing incompatible columns or relabeling Engage registration
  diagnostics as native instance evidence.
- Mixed search consistently puts known clocks before unknown clocks, including
  early native instants and explicitly known Engage Go-zero. This intentionally
  changes the exceptional legacy unknown-before-pre-year-one order; normal
  chronology and equal-clock family precedence remain unchanged.

## [0.9.10] - 2026-10-14

### Fixed

- Content retries, recovery commands and post-sign-in follow-ups keep the selected archive and shell-quoted arguments instead of silently targeting the default archive.
- Generic Stream sign-in continues to stored source choices rather than advertising a default transcript fetch that could silently replace an explicit requested selection.
- Rejected resident package kinds offer format-inspection guidance and a qualified changed-selection route where applicable, rather than repeating the unchanged rejected selector as its fix. Original requested-kind evidence and selected document references remain intact.

## [0.9.9] - 2026-10-14

### Added

- Explicit `content capture --file FILE` or `--from document-observation:N` for
  guarded already-resident DOCX/XLSX/PPTX supported text and cached-value observations.
  Offline content reads and shared search retain immutable native snapshots,
  separate source-byte/representation digests, exact clocks and per-attempt origins.
  Compound unreadable is not verified format/encryption; readable empty supersedes
  earlier text. First100 copy-only previews include full-record archive SQL and
  independent local source coverage. No hydration, Office launch, decryption,
  recalculation, discovery or Linux acquisition is added. Schema17 is additive.

## [0.9.8] - 2026-10-13

### Added

- Explicit `content fetch page/stream` and user-agreed `content signin`, with canonical ticket-free retries and owned browser closure before atomic archive publication.
- Immutable native file/account-qualified SharePoint page projections and selected Stream bodies, retained last-good text with nullable current selection, and offline `content list/search/show/sources` plus shared `search --source content`.
- Archive schema 16, source coverage outside shaping/global limits, bounded first100 structured previews, exact archive-only SQL follow-ups, and honest unqualified native Windows acquisition boundaries.

### Fixed

- Validate current and retained cloud observation bindings even at zero hits, mark superseded native incarnations retained, and preserve escaped site identity during explicit sign-in.
- Make content sign-in help, network/privacy contracts and cross-source summaries describe the explicit content workflows and archive-only reads consistently.

## [0.9.7] - 2026-10-13

### Added

- `--fields app_summary` adds `losses_total`: the sum of the counts in `app_observation.served.losses`, null when nothing is served. The text cell adds `N native fields not read` when it is above zero.
- Text `search` names each source whose match count is a floor because it stopped reading at `--limit` (`matched_at_least` in JSON), and `search --help` says the count is a floor, not a total.
- Text `loop list` and `loop search` print `partial snapshots of cached text, not full or current pages` under a table that returned rows.
- The help of `--since` and `--until` on `docs list` and `docs search` says which rows they never match.
- `docs sync` reads OneDrive's local usage file: the Mac default files of each Business account folder, or one ordinary local copy given with `docs sync --onedrive-usage-file FILE` on Mac or Windows. Each recently used file, each formatted variant of it and each frequent-collaborator profile is a document observation with `variant` `onedrive_usage`, application `onedrive` and the file's own record in `usage_native` (`Type` `recent`, `document` or `collaborator`). One native ID can have a recent row and several document rows; none is preferred or merged. Nothing the file refers to is opened.
- A usage document row carries the sharing claims OneDrive recorded. `History` previews the first 100 sharing instances and each instance its first 100 participants, with the full captured counts, the number the file itself states and explicit truncation flags. `full_inspection` is an archive-only `sql` command that prints the row's complete stored record.
- Usage clocks are `{Raw, Value}`: the file's exact text, and UTC or null. `modified_at` is the file modification time only, so `--since`/`--until` never match an access, sharing or capture time.
- `docs show` of a usage row adds `usage_relations`: the rows of the same capture with the same native ID.
- `docs list` and `docs search` accept `--application onedrive`. Shared `search` returns usage rows with the other document metadata.
- A default `docs sync` tries the OneDrive usage files independently of the Office JSON caches and the Office registry. A `discovery` entry can now carry a `locator` when it is about one default location, such as an account folder whose name is not read.
- A usage source whose first capture failed has no source ID, so its `fix` names the retry `m365crawl docs sync --onedrive-usage-file FILE` with the file's path, quoted so that pasting it cannot run text from the file name.
- The archive moves to schema 15: additive `usage_json`, `native_key` and `members_capture` columns and a `document_usage_relations` table. Existing rows are not rewritten. The first command that writes the archive upgrades it: any sync, and any read that refreshes first. Only `--max-age 0` reads and `docs sources` leave a schema 14 archive as it is. After the upgrade an older m365crawl cannot open the archive (`archive_newer`).
- A document capture that found its file unchanged no longer copies the list of rows it holds: it points at the earlier capture that holds them (`document_capture_metadata.members_capture`), so repeated syncs of an unchanged Office JSON, registry or OneDrive usage file add no member or relation rows. In raw SQL, the rows of capture C are the `document_capture_members` rows whose `capture_id` is `coalesce(members_capture, capture_id)` of C; `sql --help` says the same.
- A read no longer copies an unchanged registry or OneDrive usage file again, however long ago it was captured: it checks the file's size and modification time and skips it. `docs sync` still captures every time.
- In the OneDrive usage files examined so far, every clock of a recent row is written without a zone. m365crawl does not guess one, so those clocks have a null `Value`, those rows' `modified_at` is null, and date windows exclude them. Recent rows with a parsed explicit-offset `FileModified` clock can match `--since`/`--until`; recent rows with missing, unparsed or zone-less file-modification clocks cannot.
- `docs list`, `docs search` and shared `search` add a `notices` sentence when a date window left out rows that otherwise matched because they have no modification time, with the exact count for each kind of row and the reason.
- `docs list --order-by opened_at` lists rows that state an opened time first, newest first; only Office registry rows state one. `m365crawl docs list --application registry --order-by opened_at --limit 5` is the five registry documents opened last. Text output names the flag when no returned row has a modification time.
- Text `docs list` has a `usage_type` column (`recent`, `document`, `collaborator`, or `-`), and the title column holds only what the file states: the native ID for a recent row and the name for a collaborator.
- The `metadata` manifest's privacy scopes add `onedrive_usage_metadata`.
- Limit: a usage file with a write-ahead log or shared-memory file beside it is refused with `onedrive_usage_input_unqualified` and never consolidated. A database in write-ahead mode stays refused however often the sync is retried.
- Limit: a file's kind is known only after it has been registered and read, so a OneDrive usage file given to `--registry-file` (or a registry file given to `--onedrive-usage-file`) becomes a failed source whose `fix` names the other flag.
- Limit: only the usage file is read. OneDrive's library and sync databases are not read yet, there is no default discovery on Windows or Linux, and only Business account folders are read by default.
- Limit: names, addresses, thread IDs and collaborator profiles in a usage row are the file's claims. They are not linked to archived people, chats, mail or accounts, and no command follows them.

### Changed

- **Breaking:** a default `docs sync` on a machine with no usable document input now fails only when all three families (Office JSON, Office registry, OneDrive usage) are unusable and no source is registered; its message and fix name all three. On Mac it can therefore exit 0 where it failed before, when only OneDrive usage files were found.
- `--root`, `--registry-file`, `--onedrive-usage-file` and `--source-id` of `docs sync` are mutually exclusive. A `--onedrive-usage-file` that does not exist, is not a regular file, or is the archive database is a usage error and registers nothing.
- `docs list` and `docs search` break ties between rows with equal modification time, title and source by a usage row's native type, ID and format before the observation ID. The order of Office JSON and registry rows is unchanged.
- Archive-only reads also preserve pre-pointer schema-15 usage captures: when `members_capture` is absent, each copied capture owns its own members and relations. No writable migration is needed to read them.
- Usage admission detects duplicate literal Type/ID/Format tuples without delimiter collisions. Sorting compares the full literal components before limits or clipping, including IDs and formats containing the legacy `native_key` delimiter. Stored keys, observation IDs and schema version remain unchanged.

## [0.9.6] - 2026-10-13

### Added

- `application_evidence_coverage` has `posted_by_app`: how many returned Teams messages name an app, by `app_id` or `app_name`, in their served evidence. `mapped` only says the claims were read. Text output prints it as `posted by an app (names an app): K of N`. When a returned message is not derived or failed with nothing served, the count is a lower bound and the note and the text line say so.
- `--fields app_summary` on `messages`, `unread`, `thread` and `search` gives each message a compact `app_summary` (`state`, `app_name`, `app_id`, `reply_to_id`, `citations_total`) instead of the whole `app_observation`. `citations_total` is every citation read from the message; it and the claims are `null` when nothing is served. Text output prints the summary as one readable cell.
- Each entry of `app_observation.served.losses` has a `meaning`, and the help of `messages`, `unread`, `thread`, `search` and `calendar event` ends with every loss code and failure code and its one-line meaning. A partial or failed message repeats the meaning of its own codes.
- `sql --help` lists every table of the archive with what it holds, shows how to list a table's columns (`pragma_table_info()`), and says that `raw_digest` is the SHA-256 of the archived raw JSON as lowercase hex.
- The global `--help` says what `--format log` prints: one `label={json}` line per result.
- `search` reports, in `sources`, what each source matched before `--limit`: `returned` and either `matched` (exact) or `matched_at_least` (a source that stopped reading at the limit). Text output prints a line such as `3 Loop hits not shown; use --source loop` for a source crowded out by newer hits, also when it returned nothing; it says `raise --limit` when the source alone matched more than the limit, and never names the source already selected. With no archive, no matched count is reported.
- Loop channel items have `latest_cached_snapshot`, the plain inverse of `retained`. Loop sources have `retry_needed`, true on a failed or cancelled source, where a retry can help.
- The overview's Loop row has `last_projected_at` and a note that says its time is the last projection, not an edit time, that channels are partial snapshots, and what an archived record is.
- `loop list`, `loop search`, `loop show`, `loop sources` and `search --source loop` fill `archive_age_seconds` with the age of the last complete sync, and text output prints it.
- `docs sync` reads the Office registry's recently-opened list: the Mac default registry files, or one ordinary local copy given with `docs sync --registry-file FILE` on Mac or Windows. Each entry is a document observation with `variant` `office_registry`, application `registry`, the registry's own claims in `registry_native` and its opened time in `opened_at` (null when the registry gives none or it is not an RFC 3339 time with an offset; the raw text is kept). The registry states no modification time, so `modified_at` is null and `--since`/`--until` never match these rows. Nothing the registry refers to is opened, checked or resolved.
- `docs list` and `docs search` accept `--application registry`. Shared `search` returns registry rows with the other document metadata.
- A default `docs sync` tries the Office JSON caches and the Office registry independently and reports a family that is absent or refused under `discovery`, with a fixed code and the supplied-input command to use instead.
- A registry source whose first capture failed has no source ID, so its `fix` names the retry `m365crawl docs sync --registry-file FILE` with the file's path, quoted so that pasting it cannot run text from the file name. A failed source stays listed until a later sync of it succeeds; there is no command to remove one.
- The archive moves to schema 14: additive `variant`, `registry_json` and `opened_at` columns for document observations. Existing rows are not rewritten.

### Changed

- Text output of application evidence is in plain words: `citations: N read from the message, M shown; K more could not be read` replaces "admitted shown" and "source rows refused", and the `full record` line says it is the stored projection as a JSON string, not the raw message. The `[app: NAME]` marker is no longer clipped with the text column.
- The help of the message commands says that `full_inspection` prints the stored projection (a JSON string to decode a second time, in a row that is never updated or deleted), that a citation's `link` is the stored value and is never derived at read time, and what `total_admitted`, `citations_count` and `refused_citation_rows` count.
- Text output of Loop rows no longer says "current": `loop list` has a `text_state` column and a `snapshot` column that says `latest cached snapshot` or `older cached snapshot`, and a row whose record left the app's cache says `archived record gone from the app's cache`. Loop rows of text `search` show `partial_snapshot`. `loop sources` text shows the retry command only on a source that needs it.
- The help of `loop` and `loop sync` says what `loop sync` can and cannot recover when the archived record is gone from the app's cache, and the help of `loop show` says it returns one channel of one snapshot instead of promising "snapshot versions".
- A failed Loop source with an earlier snapshot now has the `effect` "the last good snapshot stays the latest cached snapshot, so its channels are still listed" (it said "stays current"), and the `fix` of a metadata-only source says that a retry now changes nothing.
- The `note` of `application_evidence_coverage` is longer: it defines `posted_by_app` and says when it is a lower bound.
- The `[app: NAME]` marker shortens a name wider than 24 terminal columns, and the overview's text note on the Loop row uses plain words instead of JSON field names.
- "Start here" and the top-level help describe `search` by every source it searches. The overview's Loop row says `Loop channels (partial snapshots)` and marks its time `(last projection)`.
- **Breaking:** document `source_coverage.scope` is renamed from `office_json_metadata` to `office_document_metadata`, because it now covers Office JSON and Office registry sources. The `metadata` manifest's privacy scopes are a separate list: it keeps `office_json_metadata` and adds `office_registry_metadata`.
- **Breaking:** a default `docs sync` whose Office JSON caches are missing or refused exits 0 when a registered source was attempted or the Office registry was usable, and reports the refusal under `discovery`; before, it failed with the refusal's code and exit 3 without trying the registered sources. When neither family is usable and no source is registered it still fails with that code and exit 3. `docs sync --root DIR` still fails on a refused directory.
- `--root`, `--registry-file` and `--source-id` of `docs sync` are mutually exclusive. A `--registry-file` that does not exist, is not a regular file, or is the archive database is a usage error and registers nothing.
- Text `docs list` and `docs search` have an `opened_at` column for registry rows; it is `-` for Office JSON rows.

### Fixed

- Reading OneNote indexes and Office files in one process can no longer release a lock another capture still holds: one gate now serializes every open and close of those source files.

## [0.9.5] - 2026-10-13

### Added

- `loop sync`, `loop list`, `loop search`, `loop show` and `loop sources` project and read the channel text of cached Loop snapshot records already in the archive (database `fluidDriverCache`, store `driverStorage.V3`). Nothing is fetched and Loop is never opened. Each row is one channel of one immutable snapshot version with a stable `loop-channel:ID`, its native sequence, pending-operation count and whether the archived record still matches it: partial text, not a full or current Loop page.
- `search` includes cached Loop channels (`--source loop`, and `--source all` without date, account, author, chat-only, mail-only or profile filters). Loop hits have no native date, so they follow every dated hit. Results that searched Loop carry a compact `loop_coverage` (totals and a short row per source), which survives `--fields`, `--limit` and zero hits; `loop sources` has the full detail.
- Every registered Loop source keeps a stable `source_id` from its first attempt, so a failed or metadata-only source can be inspected with `loop sources` and retried with `loop sync --source-id N`. A failed refresh keeps the last good snapshot.
- `loop sync` registers at most 128 new archived records per run, one value at a time, and never fails on a larger backlog: the result says `discovery_remaining` and to run it again. Records it cannot register are counted and named by `records` rowid only.
- The bare `m365crawl` overview has a `loop` row (archived records, registered, failed and metadata-only sources, channels, last projection) and a "Start here" line. Text `loop show` prints the snapshot evidence under the row.
- The archive moves to schema 13: additive Loop registration, snapshot, loss, channel and full-text tables. Existing rows are not rewritten.

## [0.9.4] - 2026-10-12

### Added

- Teams message results (`messages`, `unread`, `thread`, chat hits of `search` and the recent messages of `calendar event`) carry `app_observation`: the app, native reply-to and citation claims of each message's archived native JSON, with a state of `mapped`, `partial`, `absent`, `failed` or `not_derived`, a bounded preview of 100 citations and an archive-only `full_inspection` command. Each result adds `application_evidence_coverage` for the Teams messages it returned, after `--limit`. `properties.botMetadata` held as a JSON string, the way Teams stores it, is decoded under the record's own strict limits, so its `replyToId` is read instead of being reported as unmapped. One message is still one item and one search hit.
- The archive moves to schema 12: immutable, versioned application evidence representations with current and last-good pointers, published in the same transaction as the message they describe. A failed current representation serves the last good one as retained.
- The first writable `sync` after the upgrade derives application evidence offline from the archive, before any capture, and reports `application_backfill`. On a large archive this first sync needs up to about 0.75 GB of temporary free disk space for the write-ahead log, which is returned when it finishes. Reads with `--max-age 0` never derive it and report `not_derived`.

### Changed

- `sql` prints BLOB columns, such as `records.raw_digest` and the new `messages.raw_digest`, as lowercase hex in every output format instead of raw bytes that JSON could not preserve.

## [0.9.3] - 2026-10-12

### Added

- The overview (bare `m365crawl`) reports documents, notes, people and transcripts beside chats, mail and the calendar, with counts, registered and failed sources, and the last capture or fetch, all from what the archive already holds. Documents and notes with nothing registered have state `not_registered` and name the sync command. "Start here" gains `docs list`, `notes search` and `people`. Under `--account` the people row counts only that tenant's Teams people, and every row `--account` does not narrow says so in its note.
- An unscoped `people` list carries a notice, in text and JSON, saying it included Teams people and mail correspondents of every account and, when people sources are registered, native Outlook observations whose account ownership is unresolved.
- `people --help` explains that `last_seen_at` is the newest time a person appears in archived data, not the last chat with you, points to `conversations` and `messages --conversation` for chat recency, names the ID kinds (`8:orgid:` people, `28:` bots and apps, `19:` chats and teams named in mentions, `everyone`, `mail:`), and says `--account` takes the `<tenant_id>/<user_id>` that `whoami` lists.
- `docs list` and `docs search` take `--application` (Word, Excel, PowerPoint, or supplied for `--root` files) and `--surface` (recent, shared, recommended, dialog); other values are usage errors that name the accepted ones. Both narrow the rows only; `source_coverage` still lists every source.
- `docs --help`, `docs list --help` and `docs show` define the four surfaces, `association_state` and `boundary` `unresolved`, the native `Sharing.Type` number, and the `modified_at`, `captured_at` and `source_observed_at` clocks, and state the list order and what a null `modified_at` means.

### Changed

- **Breaking:** the overview JSON no longer carries the top-level `note` about registered native Outlook people observations or the `next` step that pointed to `people sources`. The people row now holds that information: its `observations` and `registered_sources` counts, and a `note` that names `m365crawl --max-age 0 people sources` when native sources are registered.
- Text output prints every archived time of day with its zone name (`2026-10-12 09:30 PDT`, and a transcript part's `10:02 PST`), so a text day is not mistaken for the UTC day JSON prints. Date-only values carry no zone.
- **Breaking:** `source_id` in `docs sources`, `notes sources` and `people sources` rows (and in each command's `source_coverage`) is now null, not 0, for a locator that has never had a capture admitted. A consumer that reads it as a number must accept null; `kind` and `locator_tail` identify such a row. Observation rows keep an integer `source_id`.

### Fixed

- `--format log` no longer fails with `invalid log label` for subcommands such as `docs list`: the log key is the command path with `_` for spaces (`docs_list={...}`).
- A failed document source is now identifiable and actionable: every source row carries `locator_tail`, text tables show `kind` and the path tail, and failed or cancelled rows carry `fix`, the remedy for their reason, and `effect`, which says whether the last good capture stays current. Every remedy that retries names plain `m365crawl docs sync`, which also reaches a source whose `source_id` is null. `notes sources` and `people sources` rows gain `kind` and `locator_tail`.
- `docs sync` no longer reports a failure for an Office recent or recommended file that is a successful service response listing no documents, in the exact empty shape observed in real files. The source is now complete with zero rows and reason `office_response_listed_no_documents`, and `source_coverage.notices` explains that the saved response listed none, not that the account has none. Earlier observations from the source stay in the archive as retained. Malformed, error, throttled and unknown responses, any response with an unexpected top-level key, and shared files without documents still fail as `office_json_unrecognized`. A dialog list or a supplied `docs sync --root` file that lists no documents gets reason `office_json_listed_no_documents` instead, with a notice that describes only the saved file.

## [0.9.2] - 2026-10-12

### Added

- Search and inspect source-qualified Office document metadata alongside messages and indexed notes. `docs sync` captures supported recent/shared/recommended/dialog caches, while `docs list`, `docs search`, `docs show` and `docs sources` preserve stable observations, original capture clocks, rolling-list history and independent coverage. Native URLs, sharing and activity remain unresolved source claims; no document bytes, implicit hydration or cloud fetch are included.

### Fixed

- Source-only text results preserve their existing notices, including why native people were excluded by an unresolved subject-account filter. JSON meanings and independent source freshness remain unchanged.

## [0.9.1] - 2026-10-11

### Fixed

- Make native people workflows self-explaining: `people --help` now exposes default-list query, limit, ordering and fields; empty lists explain their independent source coverage; the read-only overview points registered people archives to an archive-only coverage command without inventing contact counts or resolved identities.

## [0.9.0] - 2026-10-11

### Added

- Capture registered native Outlook people with `people sync`, search their retained observations in `people --query`, inspect stable `person-observation:ID` details and source coverage. Preserve differing native copies and contradictions without choosing current contacts, merging by email or inferring subject tenancy. Mac HxStore discovery and explicit supported copies only; Windows OWA people discovery and subject-scoped resolution remain unsupported.

## [0.8.0] - 2026-10-11

### Added

- Capture and read local OneNote indexed notes with `notes sync/list/search/show/sources`, stable note IDs, indexed text/OCR, native hierarchy and source coverage; shared search joins note hits to chats, mail and fetched transcripts without inventing authors or account ownership.
- Protect rollback-mode source indexes with SQLite-compatible shared locking and private copies; retain last-good page text and flags across partial/failed captures, keep source attempt history, and independently refresh registered locators. Default discovery is Mac OneNote; supported explicit index copies work on Mac, Windows and Linux. Live WAL capture, Windows native app discovery, page web URLs and original visual layout remain unsupported.

### Fixed

- Measure retained LevelDB entry overhead in an isolated test process, so fixture construction and prior tests cannot inflate the pre-load heap baseline. Accounting constants and calibration tolerances are unchanged.
- Mail-thread cancellation tests now use explicit cancelled contexts and a real post-seed conversation query instead of relying on platform-dependent context error polling.
- Windows installation and supported-platform guidance now describes shipped new Outlook cached mail instead of the superseded unsupported-platform error.

## [0.7.0] - 2026-10-11

### Added

- Added a bounded internal mapper for native Teams app/reply metadata and positioned citation observations, preserving message provenance and explicit malformed/unconsumed-content losses. It does not identify Copilot by name, capture private history or add archive/search/CLI citation support.
- Add a bounded native Engage Home-access-session capture backend with service-scoped viewer/network evidence, ordered thread-starter text and explicit content omissions. Observations are incomplete and do not establish linked-thread retrieval, complete feeds, shared archive or CLI support.
- Added an internal bounded OOXML reader for source-ordered Word paragraphs, native Excel cell values and PowerPoint slide/notes paragraphs, with explicit unsupported compound packages and content-loss states. This is backend groundwork, not document discovery, placeholder hydration, cloud fetching or installed document commands.
- Added a bounded internal reader for cached Loop legacy channel text with native snapshot sequences, pending-operation counts and explicit missing/unsupported coverage. This backend does not reconstruct current page layout, apply Fluid operations, fetch components or add archive/search/CLI support.
- Added a bounded internal reader for consistent Office recent, shared, recommended and open-dialog JSON copies, preserving source-qualified document, person, activity and sharing observations with explicit losses. This backend does not yet add document discovery, sync, search or CLI commands.
- Added a bounded internal reader for native Office recent-local-document registry copies, preserving source-native IDs, application and raw path/time evidence with explicit losses. It does not open referenced files or add document discovery, sync, search or CLI commands.
- Added a bounded internal reader for consistent OneDrive ListSync copies, preserving scoped file/folder inventory and native site-user evidence with explicit unavailable-source and row losses. It does not add general Microsoft Lists scraping, downloading, archive integration or document commands.
- Add a bounded supplied-copy reader for native OneDrive sync-engine file/folder inventory, scope/policy links, graph display-name observations and raw hydration history. This is backend groundwork only; live SQLite snapshots, local-file availability, document extraction, shared archive and CLI support are not included.
- Added a bounded internal reader for consistent OneDrive FileUsageSync copies, retaining separate native recent-file, formatted-document variant, sharing-history and collaborator observations with explicit losses. It does not add discovery, downloading, archive integration or CLI commands.
- Added a bounded internal reader for caller-supplied, consistent OneNote full-text index copies, retaining native hierarchy, indexed text/OCR, flags and explicit source losses. This backend does not yet add OneNote sync, search or CLI support.
- A bounded backend for native full Outlook attendee observations and detail-link evidence, preserving source copies, ambiguity, unknown fields and framing losses. Shared calendar and CLI integration are not included.
- Internal Outlook people reader maps the measured local people layouts, preserves ambiguous source copies, and reports malformed or resynced observations with bounded retention. This is backend groundwork; people enrichment is not yet connected to sync or commands.
- A workflow-first GitHub prompt-request form for additional integrations, with optional technical evidence and explicit public-data privacy reminders.
- Add a bounded native SharePoint capture backend for referenced modern-page rich text and non-meeting Stream transcripts, preserving service-scoped identity, explicit omissions and guarded offsets. Public archive and CLI integration remain separate work.
- Expose new Outlook OWA cached mail on Windows through mail commands, search, overview, status and doctor. Add nullable recipient roles and conversation identities while preserving legacy Mac fields and IDs. OWA calendar is not read in this release; Windows calendar parity is next release.

### Changed

- Contributors now add a changelog fragment, `changes/<short-slug>.md`, instead of editing `CHANGELOG.md`, so pull requests that land at the same time no longer conflict. A release assembles the fragments into its version section with `scripts/changelog-assemble.sh`, and CI refuses a pull request that adds lines under `[Unreleased]`.
- Report exact zero-count statement ranges from failing source files when the Unix function-coverage gate fails, without changing coverage thresholds or allowlists.
- Reframe the README around agent recall, triage and meeting preparation, add an installed-guide onboarding path, and clarify offline archive reads, explicit transcript fetching and the no-app-storage-write boundary.
- Clarify the usage-led expansion's deferred Planner/To Do and private Copilot coverage, version-qualified cached Loop direction, and unqualified live OneDrive sync-cache capture. Backend readers are not advertised as connected or installed support.
- README explains the usage-led integration priorities, separates planned and deferred sources from shipped support, and invites agent-assisted GitHub issues ("prompt requests") for additional support.

### Fixed

- Synchronize the synthetic browser write-failure test with server-side connection release, so coverage cannot finish before the failed response-write handler exits.
- Make the OneDrive ListSync scope-loop cancellation regression deterministic despite background database context checks, without changing the reader.
- Exercise transcript sign-in wait cancellation directly in a deterministic synthetic test, rather than depending on a timer cancelling at that stage of a full command.
- Windows browser shutdown retains bounded, exact-owned process-generation handles and checks their exit signals before reporting completion. Profile/job membership alone no longer establishes completion; native failures stay explicit, with no additional teardown grace.

## [0.6.2] - 2026-10-09

### Added

- Each list command's `--help` now ends with the `--fields` keys it accepts, so an agent can pick them before running the command instead of learning them from an error. The list comes from the same table the check uses. `calendar agenda --help` also names the keys that only `calendar event` has.
- A mistyped or guessed command name now suggests the command that does the job: `chats` suggests `conversations`, `email` suggests `mail`, `events` and `meetings` suggest `calendar`, and `transcript` suggests `transcripts`. The error's `fix` names that command's `--help`. These words remain errors, not aliases.
- The calendar agenda now says how fresh Teams' own calendar cache is. Each account in `accounts` has `teams_cache_fresh_at`, when the Teams calendar cache last synced with the calendar service, and, when the requested range starts after that time, a notice says that an event created or changed since then, such as a new invitation, is not listed until Teams refreshes its calendar (open the Calendar in Teams, then sync).

### Fixed

- `transcripts fetch` and `transcripts signin` no longer fail on Windows when Edge's first process hands off to another and exits before the browser is ready; m365crawl waits for the browser and still closes every process it started, including any outside the job object.
- `transcripts signin` opens the window on a site of the host, as `transcripts fetch` does, so signing in to a OneDrive host no longer waits on a page that left the host; a signed-in user who cannot open that site is still recognised.

## [0.6.1] - 2026-10-09

### Fixed

- Transcripts stored in a meeting organizer's OneDrive now fetch, where v0.6.0 reported that the SharePoint host redirected elsewhere. `transcripts fetch` now opens each host on its first part's site instead of the host's root, which on a OneDrive host leaves the host.

## [0.6.0] - 2026-10-08

### Added

- Meeting transcripts. `transcripts` lists recorded meetings and their transcript parts; `transcripts show <meeting>` prints a meeting's transcript, every part in time order with each seam marked. A meeting can be named by its call, its Teams chat, its calendar event or a Teams link. Both read the archive offline.
- `transcripts fetch <meeting>` (or `--since DATE`) downloads the parts not yet stored through an invisible Edge or Chrome that m365crawl starts with its own browser profile, and stores them; stored parts are not fetched again. It reaches only SharePoint hosts and never sees a password or token.
- `transcripts fetch` signs in silently where the organization's single sign-on covers the browser. Where it cannot, `transcripts signin` opens that browser profile in a visible window once so the user can sign in. Without a terminal it refuses unless given `--user-agreed`, so an agent asks the user first, and it prints each step. If the window asks to enroll or register the device, the organization signs in only from managed devices, and the agent tells the user instead of retrying.
- Transcripts in `search` (one result per part with a `matches` count; `--source transcripts`; `--from` matches the speaker), in `calendar event` (a meeting's recorded calls and what is fetched), in `status`, and two `doctor` checks (a browser is available; the browser profile is private).
- Teams links everywhere: `thread`, `transcripts` and every `--conversation` flag accept channel, chat, message and meeting links from teams.microsoft.com, teams.cloud.microsoft and teams.live.com.
- Calendar items carry `id` (equal to `event_id`); `mail unread` has `unread_total`.

### Changed

- `transcripts fetch` and `transcripts signin` are the only commands that use the network, and only through the browser m365crawl starts; every other command is offline.
- The archive format is version 7, which adds the transcript tables. A v0.5.0 archive is upgraded the first time this version syncs it.

### Fixed

- Outlook's general folder type was read as "Junk or To Me", so user folders and several system folders were labelled `junk`. They are now `other`; To Me is still recognised; the next sync corrects an existing archive.
- A recording Teams re-posts later, or whose transcript notice falls near a different occurrence, stays with the one occurrence it belongs to.

### Security

- Built with Go 1.27.2, which fixes standard-library vulnerabilities in `net/http`, HTTP/2, `crypto/tls` and `net/textproto` (GO-2026-6603, GO-2026-6605, GO-2026-6607, GO-2026-6608, GO-2026-6610 to GO-2026-6613, GO-2026-6617).

## [0.5.0] - 2026-10-08

### Added

- Outlook mail. `sync` now reads the new Outlook for Mac's mail into the same archive as Teams chats and the calendar: messages, folders, senders and recipients, read and flag state, message bodies, and each attachment's name, size and type (the files themselves are not copied). The new `mail` commands read it: `mail list` (filter by folder, sender, date, unread, flagged or with attachments), `mail show`, `mail thread`, `mail folders` and `mail unread`. Every mail list says when the archive last synced and how far back Outlook's local copy reaches, because Outlook keeps only recent mail on the Mac. Mail is not read on Windows yet; there the `mail` commands say so and point at what works.
- Cross-source search. `search` looks through Teams chats and Outlook mail together, newest first. Each result says where it came from (`source`), `--source chats` or `--source mail` searches one of them, and `--folder` searches one mail folder.
- Meeting prep. `calendar event` lists the mail about a meeting (the invite first, then messages with the same subject exchanged with attendees around the meeting) and the latest messages in its Teams meeting chat. `people` also lists the people you exchange mail with.
- A self-explaining overview and help. `m365crawl` with no command shows what the archive holds for chats, mail and the calendar, how fresh each is, and what to run next. `--help` opens with a short "Start here" list, every empty result says why it is empty, and a mistyped command suggests the right one. The agent guide (`m365crawl skill`) is much shorter and points at `--help`.

### Changed

- The archive format is version 6, which adds the mail tables. A v0.4.0 archive is upgraded the first time this version syncs it.
- `doctor`'s snapshot now has one line per source: Teams (chats, channels, teams, meetings, messages, people, recordings and transcripts), Mail and Calendar.
- A faster archive: syncing a mailbox that has not changed writes nothing and indexes nothing again, and a message body is compressed and converted only when the message is first stored or its body first arrives.

### Fixed

- Lower memory: reading Outlook takes much less memory than before (on a large synthetic mailbox, peak memory fell from 250 MiB to 137 MiB).
- Reliability: a damaged record in the Teams cache can no longer make a sync hang.

## [0.4.0] - 2026-10-07

### Changed

- The new Outlook for Mac calendar source is read by default. When the default Outlook directory exists and holds a profile, `sync` and the read commands read it with no flag. `--outlook-root none` (or `M365CRAWL_OUTLOOK_ROOT=none`) turns it off, and `--teams-root` without `--outlook-root` still keeps it off, so tests and fixtures stay hermetic. `M365CRAWL_OUTLOOK=1` is still accepted and now does nothing. A default Outlook never fails or holds up a Teams sync: no Outlook directory or profile says nothing, and a profile that cannot be read (no Full Disk Access, a store this build does not read) is a source with the new status `unavailable` and its `error`, with no change to the run's status or exit code. A named `--outlook-root` keeps its failures (`no_outlook_profiles`, `outlook_root_missing`, a failed source). The `doctor` `outlook_store` check passes when there is no Outlook or no profile and when a profile reads normally; it warns only for a named root with no profile, denied access, an unknown store version or a failed read.
- `--outlook-account none` now records the ended link even for a profile that had none, so it stays unlinked when the automatic link below is on. An archive written before this has no such row until the flag is given again.

### Added

- Automatic link by proven identity. A sync links an Outlook profile to a Teams account (method `address`, shown as `link` by `calendar sources`) when an address of an account signed in to the profile equals that Teams account's own address, ignoring case: the Outlook read keeps the addresses its account objects hold (class 0x49, the string at +5532), and the Teams archive holds the signed-in user's `userPrincipalName`, `mail` and `email`. It links only when exactly one Teams account has one of them and no other profile claims that account or address; no match, two Teams accounts with one address and two profiles with one address leave the profile unlinked with the `unlinked_accounts` notice. An explicit `--outlook-account` link or `none` always wins and is never replaced. An archive written before this reads an unchanged Outlook store once more to get the addresses. A default user no longer sees every Teams meeting twice.

- Outlook gone detection. An event the Outlook store no longer holds is now marked gone (`removed_at`, all data kept) when two consecutive syncs, reading different copies of the store, both miss it (one miss is only remembered, so a torn copy cannot hide a live meeting), and counted in the sync report's `calendar.gone`. Experiment E11 on a real store showed that Outlook writes no deletion marker: a deleted event's objects stay readable and unchanged until Outlook compacts the file, and then they are gone, so absence is the only signal and a deletion shows up late (see "Deleted events" in docs/outlook-store.md). The rule is cautious: it runs only on a read that lost nothing (no `outlook_blocks_damaged`, no `calendar_unmapped`), never marks a series master, never judges an event that starts more than 60 days before the read (Outlook's own window reaches back about 99 days and drops older events without a deletion), and remembers nothing when a read would drop more than 20 events and more than 10 percent of the live ones. A marked event is live again when a later read holds it. `calendar sources` now reports `deletions: "inferred"` for Outlook rows (it was `"unverified"`).

### Fixed

- Outlook events no longer take their location and body preview from the subject. The store writes an absent string as offset word 0 and length word 0, and offset 0 is the first string of the event's string area, which is the subject as first written; the mapper read every string by its offset word alone, so an event with no location or no body got the subject (or, after a rename, the old subject) for both. It now reads a string only when its length word is non-zero, so such an event has an empty `location` and `body_preview` (a probe appointment with neither had both equal to its subject, and kept the old subject after a rename). On a real copy of 3,374 live Outlook events, every one had a location and a body preview before (318 and 101 of them equal to the subject); now 3,027 have a location and 3,272 a body preview, and none equals the subject. `outlookcal.MapperVersion` is 5, so an archive re-derives its Outlook events on the next sync. The layout is recorded in `docs/outlook-store.md`.
- Docs: all-day Outlook events are at midnight UTC with the zone name `UTC` (the store's own label); `start_date` and `end_date` are the dates to read, in SPEC.md and the skill guide.

## [0.3.1] - 2026-10-07

### Fixed

- V8 decoder: an array with named properties is stored in the reference table as the final value, so a later back-reference to it keeps the properties. Before, `[a, a]` with `a = []` and `a.x = 1` decoded the second element as a bare array and lost `x`. A reference made while the array is still being read (a cycle) still holds the bare slice, which the canonical form already identifies with the array.
- IndexedDB decoder: a snappy envelope that declares more than 32 times its compressed length is refused before it is decoded, as the new omission `snappy_too_large` (listed in SPEC.md). Before, `ff 11 02 ff ff ff ff 0f` made the decoder allocate 4 GiB.
- Release: notarization waits at most `NOTARY_TIMEOUT` (default 20 minutes) per binary and then fails with the reason. Before, `notarytool submit --wait` had no bound, so a slow Apple notary queue ran the release job into its 60-minute timeout with no explanation (the first v0.3.0 run). A failed run leaves the tag and no release, and the next run of the Release workflow resumes from the tag.
- Skill guide: says that `--to` is exclusive and that a signed offset such as `--from=-14d` counts from now, not from local midnight; that `recordings` and `recording_count` cover recordings, transcripts and call events (`kind`), not recorded meetings; what `matched_by` `recap` and `window` mean for trust; and when `mine_basis` (with `mine: true`, or `ambiguous` with `mine` absent) and `mine_ambiguous_omitted` (omitted when zero) appear.

## [0.3.0] - 2026-10-06

### Added

- A missing Outlook root is one coded error. `--outlook-root` (or the default directory of `M365CRAWL_OUTLOOK=1`) that does not exist, is not a directory or cannot be read now fails `sync` and every read command (the implicit sync included, even when it would be skipped) with `outlook_root_missing`, exit 3 and a `fix`; before, `sync` failed as `internal` ("a bug in m365crawl") and `calendar` carried on with a warning. A root that exists but holds no profile keeps `no_outlook_profiles`. `whoami`, `status` and `version` are not stopped by a bad root, and `status` reports it as an `outlook` object with the same `code`, `message` and `fix`.
- `notices` on `calendar`, `calendar event` and `calendar actions`: when the result holds an event that Outlook holds and the Outlook source is off for the run, a notice says the Outlook data is archived and not refreshed and gives the age of each Outlook account's last good read. Text output prints it as one dim line.
- Gap attribution: with several accounts in scope, each `accounts` entry of the calendar reads says which uncovered days it lacks: `uncovered_all: true` when it lacks every day of `uncovered_days`, or its own `uncovered_days` (and `uncovered_days_total`) when it lacks fewer, so an unlinked Outlook profile that covers fewer or more days no longer hides which account the gap belongs to. The day list is never printed twice.
- `calendar sources`: one row per account and source with `principal`, `link` (`config` or `none`), covered days and `last_verified_at`, event counts (live, removed, with detail, attendees, body, online), recap counts (rows with content, linked, action items) and `unknown_time_zones`. An Outlook row adds `status` (`ok`, `skipped_interval`, `unsupported_version`, `unsupported_layout`, `unreadable`), `error`, `last_read_at`, `last_attempt_at`, `next_read_after`, `read_interval_seconds`, `unknown_layouts` (only event and detail objects of an unknown tag; the reader no longer reports every other class of the store as unknown), `blocks_invalid_ratio`, `unmapped_values` and `deletions: "unverified"`. A sync keeps the census of each good Outlook read and whether it skipped the read (`meta.outlook_read:<account>`, `meta.outlook_skipped:<profile>`). `doctor` gains the `outlook_store` check (a warning, never a failure): the source is off, or each profile's store version and last read, the missing-profiles hint and the last failure. `calendar_cache` now judges the Teams calendar only.
- The Outlook link: `--outlook-account none` ends a link whose profile directory is gone (the profile names also come from the archive's links), an event id printed while a profile was linked still resolves after the unlink, `--outlook-account` with `--max-age 0` is a usage error that names the `sync` command to run, and `M365CRAWL_OUTLOOK_ACCOUNT` is read only by commands that sync or read the calendar (a warning when the Outlook source is off).
- Outlook twins now merge with Teams events on a real cache. Teams writes an event's iCalUID in lower-case hex and Outlook's store holds the same id in upper case, so no key matched (0 of 149 on a real copy; 148 of 149 match once the case agrees). The Outlook mapper now lower-cases the id and the series key derived from it, so Teams keys and every Teams `event_id` are unchanged, and `outlookcal.MapperVersion` is 4, so an archive re-derives its Outlook events on the next sync. The re-derive replaces the old rows: an Outlook row whose key the batch now has in another case is deleted (with its match row), so no event is listed twice; an event Outlook no longer holds is kept. The Teams fixture's ids are regenerated in lower case (a new `event_id` for each fixture event), and the Outlook fixture keeps its upper-case ids, so a test that merges them proves the case rule.
- Outlook in `calendar` and the explicit account link. `--outlook-account <tenantId>/<userId>` (`M365CRAWL_OUTLOOK_ACCOUNT`) links an Outlook profile to a Teams account, `none` ends the link, and `--outlook-profile NAME` (`M365CRAWL_OUTLOOK_PROFILE`) names the profile when the root holds several. Linked, the twins of the two sources merge into one item with `sources: ["teams","outlook"]` and `filled_fields`, an Outlook-only event gains the account's `tenant_id` and `user_id` (its id changes and the old one still resolves), coverage is judged per person, and a meeting Teams marked gone stays hidden. Linking is explicit only; a refused link is a `usage` error and the sync report is still printed. The agenda's `unlinked_fix` and its text line name the exact command that links each unlinked profile, and `calendar event` says when only Outlook holds an event and what is not known.
- A meeting recap with no iCalUID now also links to the one event that starts within five minutes of its meeting start (link method `start_time`), because a recap's end is when the call ended and rarely matches the scheduled end. Cancelled and declined events are never candidates. Start links are recomputed on every derivation (cleared, then made again from the current events), so a link never depends on arrival order and a link whose event went or is no longer unique is dropped; id links are never touched. A recap with no such event, or with several, stays unlinked. Sync's `calendar` counts gain `linked_by_start`, `unlinked_no_event` and `unlinked_ambiguous`, a census after the derivation rather than what the run changed. `teamscal.MapperVersion` is 3, so an existing archive links once.
- `calendar`: an unlinked recap carries `placed_at` and `placed_by` (`meeting_start` or `recording_start`) and the text says when the meeting start is unknown, instead of printing `-`. `link_method` on a recap is documented: `ical_uid`, `time` or `start_time`.
- Outlook as a second calendar source behind `--outlook-root DIR` (`M365CRAWL_OUTLOOK_ROOT`, or `M365CRAWL_OUTLOOK=1` for the default directory; off by default and off when `--teams-root` is set alone). The sync copies the new Outlook for Mac store at most once per five minutes, maps its events with the Outlook mapper and fills the calendar tables for the account `outlook/<profile>`; the source status `skipped_interval` and the losses `outlook_blocks_damaged`, `calendar_unmapped` and `calendar_refused` are new in sync reports. A mapper version change reads an unchanged store again and re-derives the stored events. A skipped read reports the last read's losses, or its failure; no profile, or one that cannot be examined, is a coded failure. No account link and no gone detection yet.
- `calendar actions [--from --to --days --owner --mine --limit]`: the action items of the meeting recaps held by the events of a range, with `owner`, `mine` and `mine_basis` (`full_name`; `first_name` for a one-word owner equal to your first name when no one else in the meeting has it; `ambiguous`, with `mine` absent and named in `unknown_fields`, when someone else does), `origin` and `expires_at`. Superseded items are hidden, the recap's own items win over the catch-up's, an expired recap is still listed, `--mine` keeps the sure matches, reports the ambiguous ones it left out as `mine_ambiguous_omitted` and the accounts with no archived name as `mine_unknown_accounts`, and without an archived own name it is a usage error that points to `--owner`. It carries the agenda's coverage keys and works on an archive without calendar tables as the other calendar commands do (empty, with `needs_sync`).
- `conversations`: a Meeting conversation that calendar events name as their chat gains `calendar_event_count` (live occurrences, distinct keys, across the accounts of the principal; cancelled ones are not counted, declined ones are) and `calendar_series_key` (the series they share; absent when there are several or only single events). A chat no event names is unchanged.
- Calendar tables (archive schema version 5): a sync derives the Teams calendar, the meeting catch-up and the meeting recaps from the generic `records` of those stores into `calendar_source_events`, `calendar_sources`, `calendar_covered_days`, `calendar_recaps`, `calendar_recap_items`, `calendar_account_links` and `calendar_matches`, inside the same transaction. The stores stay in `records` and `stores`. Events keep the richest copy Teams ever served (a thinner later copy never erases attendees, body or rooms), a record that leaves the cache marks its event gone only when the cache still holds that day, covered days and the window never shrink, and recaps link to their event by iCalUID or by time. An archive upgraded from schema 4 fills its calendar on its first sync from the records it already holds, without the cache. `m365crawl calendar` and `m365crawl calendar event` read them; `m365crawl sql "select count(*) from calendar_source_events"` also works.
- Sync reports and `sync_runs.counts_json` gain `calendar` counts (`events`, `recaps`, `recap_items`, `gone`, `linked`, `refused`), and the text report a row for each. New omission codes: `calendar_unmapped` and `calendar_refused` (losses), `calendar_unknown_time_zone` and `calendar_all_day_unaligned` (counted, not a loss).
- New commands `calendar` (the agenda for a range, merged across sources by principal, with per-principal coverage) and `calendar event <event>` (one event with attendees, body, recaps, action items and the recordings that belong to this occurrence). See docs/commands.md. Items carry `account_id`, `sources`, `unknown_fields`, `filled_fields` and `removed_by`; a flag the source stated prints `true` or `false` and an unstated one has no key. Recaps that no event holds are listed under `unlinked_recaps`.
- Meeting recaps (store `meeting-recap-catchup`) are mapped from the record's `data` object. A real cache wraps every record as `{data, recapId, syncTimeUtc}` and all 17 measured records were counted `calendar_unmapped` ("no callId") because the mapper read the top level. The flat shape, which only the old fixture had, is no longer accepted: a record without a `data` object is unmapped ("no data object"). `teamscal.MapperVersion` is 2, so an existing archive re-derives its recaps on its next sync from the records it already holds. The fixture's recap records are regenerated in the wrapped shape.
- Calendar derivation, reviewed: an event is gone only when no profile that holds its account has it live (the same on a sync and on a rebuild); a sync moves `synced_at` and covered-day freshness only for the accounts it read (a rebuild from the records moves none); `calendar_unmapped` and `calendar_refused` are counted on every sync, including one that finds the cache unchanged and the upgrade sync, so those are `ok_with_omissions` and no longer `unchanged` (the reasons are kept in `calendar_losses`, as field names and shapes, never content); a calendar failure is rolled back to a savepoint, counted as `calendar_derivation_failed` and retried by the next run, and no longer fails the source; the derivation stamp carries the time zone, so a zone change rebuilds (and takes covered days again); calendar tables that are empty under a current stamp while calendar records exist are rebuilt; an index on `calendar_source_events(source, account_id, source_id)` keeps the first derivation linear (10,000 events took 19 seconds, 20,000 took 95).
- A time-zone change takes the covered days again from removed records as well as live ones, so a day whose events were evicted stays covered and the window keeps its span.
- `doctor` gains the `calendar_cache` check: a warning when an account has no Teams calendar database in the archive or the newest calendar cache time is more than 7 days old.
- `teamsdesktop.RulesVersion` and `RulesStamp`: when the denylist or scrub rules change, the next sync blanks and rebuilds the derived calendar tables from `records`.
- Internal: the synthetic fixture (`testdata/teams-fixture`) gains the Teams calendar databases the planned calendar mirror will read: `Teams:calendar` (stores `calendar` and `calendar-internal-data`), `Teams:meetforwork-manager` (`meetforwork-meeting-catch-up`) and `Teams:meeting-recap-manager` (`meeting-recap-catchup`), plus call recordings, a transcript and a call event in each account's Meeting conversation. Per account the fixture now holds 60 messages (was 55), 14 calendar events, 3 calendar bookkeeping records, 5 catch-up records and 2 recap records; `records` holds 60 generic records in total (was 12). No product behavior changed: the new stores are mirrored generically, so `stores` lists them and `records` returns them. The shipped sample output in the README is refreshed to the fixture's current counts.
- A weekly `Credential health` workflow checks that every release secret is set and that the tap deploy key still authenticates, and reports a failure as an issue.
- Internal: `internal/outlookdesktop` finds the new Outlook for Mac profiles, takes a consistent private copy of `HxStore.hxd` (read-only toward Outlook, retried when the store changes during the copy, a typed store-busy result after three attempts, free space checked first, removed on every exit path) and fingerprints the live file. It is not wired into any command yet.
- `sync --full-read` (and `M365CRAWL_FULL_READ=1`): read every record of every source in full instead of skipping the ones whose bytes have not changed, even from a cache that has not changed since the last sync (it does not stop at the fingerprint shortcut, so it checks the skipping exactly when the cache is stable). The archive is the same either way; it is a check. Under `watch`, the variable makes every sync that `watch` runs a full read.

### Changed

- The Outlook read uses less memory. Detail objects no event links are dropped, and each detail object and event copy is freed as soon as its event is mapped, so the reader never holds all of them and all the mapped strings together (the peak live heap on the benchmark store falls from 39.8 to 25.3 MB). A string the scrubber leaves unchanged is no longer decoded into a second copy. The sync hands back freed memory before the read, collects at a 25% heap target during it and hands back its garbage after. The stored data and `MapperVersion` are unchanged.
- A recap linked to an id that several occurrences of a series share now attaches only to the occurrence whose start is nearest its `meeting_start` within five minutes (the `start_time` tolerance), chosen among all occurrences of the id including removed, cancelled and declined ones, instead of to every occurrence. A recap no occurrence matches, or with no meeting start, stays on all of them and says `series_level: true` in `calendar event`. `has_recap` and `action_item_count` follow the same rule. No stored data changes.
- The archive schema version is 5: version 4 adds read memory (below) and version 5 the calendar tables. An archive from v0.2.0 (version 3) is upgraded when it is next opened for writing. An older m365crawl cannot open a version 5 archive (`archive_newer`).
- Releases start from code state. Merging a release-notes file `docs/releases/vX.Y.Z.md` (or `vX.Y.Z-rc.N.md`) to `main` is the release. The pipeline reads the state of `main`, not the push: it releases the lowest notes file without a finished release, so a refused, failed or replaced run is retried by any later run (a fix-up merge, a `CHANGELOG.md` change, or a manual start) and notes merged back to back all release in order, one per run. It refuses a wrong notes file name, a tag at the wrong commit and a stable version with no changelog section, creates the tag itself after the signing secrets check, and runs every job in the same workflow run. Pushing a tag by hand no longer releases anything. A rehearsal (`-rc.N`) also pushes its cask to the tap's `rehearsal` branch (never `main`), the release scripts have a `--selftest` that runs in `make lint`, and a rehearsal release carries a cask asset like a stable one. Versions only move forward: a notes file at or below the highest released stable version is refused. SSH to the tap verifies GitHub's pinned host keys.
- A failed `release`, `verify-release` or `verify-homebrew` marks the GitHub release as a prerelease so it is not "latest" (a later good `verify-release` restores it), and a failed `verify-homebrew` puts the tap's `main` back to the previous cask with a new commit. Any failed or cancelled job of the release or credential-health workflow opens (or comments on) an issue with the workflow, the outcome, the version and the run link.
- The release flags are settled in their own job after every darwin and Windows verification job, and the cask is published only after it, so re-running one flaked verification job can no longer publish the cask for a release still marked as a prerelease. A patch for an older line (1.2.4 after 1.3.0) is refused by design.
- Dependabot keeps GitHub Actions and Go modules current (weekly, grouped). Only Go module updates that are semver patch or minor auto-merge (squash) when the required checks pass; GitHub Actions updates and majors stay open for a person to read.
- A sync of a changed cache skips the records whose stored bytes have not changed since the last committed sync, so a re-sync does work in proportion to what changed: on a real cache of about 62,000 messages a re-sync dropped from about 15 s and 550 MB peak memory to about 4 s and 380 MB. The rows, the report counts (`seen`, `inserted`, `updated`, `unchanged`, omissions, `redacted`), the `watch` changes, the `removed_at` marks and the people are exactly what a full read gives. This is archive schema version 4: `records` gains `raw_digest` and `value_redacted`, and a `typed_memo` table remembers each typed record's digest and effects. A version 3 archive is upgraded in place; its first sync after the upgrade reads every record. The memory costs about 11 MiB on that cache (2.5% of the archive). Builds that know schema version 3 refuse a version 4 archive: `sync` fails with the coded `archive_newer` error, while the read commands report a `db_error` whose message contains `archive_newer`; v0.1.0 and earlier report a plain `db_error`. Anything that changes how bytes become rows (`DecoderVersion`, the derivation version, any entry of the scrub rule tables or the deny list: the signature hashes the tables themselves, so a new key name, pattern or term is enough) makes every record be read in full again, and the records are scrubbed again under the new rule. The memory is bounded by the cache, not by the archive's age: records that left the cache are forgotten at the next sync, and a sync loads one database's memory at a time. Records that share a row (generic keys that scrub alike, or two records carrying one message) are read in full together, so the row and the counts match a full read on every sync; a pair of typed records that rewrite one row costs one extra read the first time it is seen and none after. The version 4 migration adds each new column on its own, in one transaction, so an archive that a crash left half upgraded repairs itself on the next open. SPEC.md section 4.1 has the rules.

### Fixed

- A linked Outlook profile no longer shrinks an event's attendees. The merge chose the attendee list of one copy by clock, and the Outlook copy is newer in 146 of 148 real twins while holding the shorter list (in all 67 twins with two lists, Teams has more), so linking made the agenda lose attendees. The attendee list and the structured room list are now the union of both copies' lists (by lower-case address, display name when there is none; rooms by name), an entry both copies hold takes the response, type and role of the copy with the newer list clock, an entry only one holds is kept, a list the union enlarged is named in `filled_fields`, and `attendee_count` follows the merged list. Other fields keep the clock rule.
- The Outlook fixture's twins now describe the same meetings as the Teams fixture: "Fixture plain event" is "Fixture planning review" and "Fixture online meeting" is "Fixture pacific sync", and the first lists three of the Teams copy's six attendees, one with a newer answer. `scripts/hxfixture` is at generator version 2 and the committed `HxStore.hxd` and provenance are regenerated; `outlookcal.MapperVersion` stays 4 (the mapper did not change) and its pinned digest moved with the fixture.
- Release: a run that resumes a published release at the publish step now runs `settle`, `publish-homebrew` and `verify-homebrew`; they were skipped because `release` is skipped in a resume. `make lint` checks the wiring with `scripts/check-release-wiring.sh`.
- A linked Outlook profile no longer overwrites what Teams knows about its own meeting. The Outlook copy is the newer one in 146 of 148 real twins, so it won the detail base and replaced the join link, short link, dial-in and meeting chat id (and with the chat id, the recordings), turned `is_online_meeting` false, and dropped rooms only Teams named. Now the Teams copy supplies the meeting links when it states them (Outlook fills only a Teams gap), an event Teams says is online stays online while its join link is kept, and `rooms` is the union of every copy's rooms, including places named only in one copy's location text. The history rule for a removed Teams meeting is unchanged.
- New `overridden_fields` on `calendar` and `calendar event` items: `[{"field", "from", "as_of"}]` names each field both copies of a linked twin stated with different values and the source whose value the merged event holds (`union` for a merged list), so a replaced value is visible the way `filled_fields` makes a filled one visible. Time zones compare by their resolved IANA zone (Teams' `PacificSt` and Outlook's `Pacific Standard Time` are one zone), and addresses without case, so a difference of spelling alone is not listed.

## [0.2.0] - 2026-10-05

### Added

- `watch --min-interval DURATION` (`M365CRAWL_WATCH_MIN_INTERVAL`, default `60s`, `0` disables): the least time between the end of one sync and the start of the next. A busy Teams cache changes constantly, and on a real one `watch` ran 28 full syncs in 10 minutes back to back at about 88% of a CPU core. File events that arrive during a sync or the pause now coalesce into one sync when the pause ends, and no sync runs if nothing changed. The first sync is still immediate. This replaces the fixed 5 second gap between sync starts. Changes reach the archive up to the interval plus a sync later; set `--min-interval 0` for the old latency.
- `metadata`: prints the crawlkit app manifest (`control.Manifest`) as JSON in every output mode, so `crawlctl discover --app m365crawl` finds m365crawl. It needs no archive or Teams cache and never syncs. No schema or existing field changed.
- `records` table (archive schema version 3): every Teams IndexedDB database that has no typed mapper and no credential-like name is mirrored into it, one row per record, with the key and the value as canonical JSON. Rows follow the archive rules: a record that leaves the cache gets a sticky `removed_at` and is never deleted; a value that fails to decode is counted and its key is still archived.
- `stores`: lists every archived database and object store with `records`, `removed` and `last_updated_at`.
- `records --database <name or prefix> [--store] [--since] [--include-removed] [--limit 50]`: lists archived generic records, newest change first, with `key_json` and `value_json` as parsed JSON. It honors `--fields` and `--max-text`.
- Sync reports and `sync_runs.counts_json` gain `records` counts. New omission codes: `denied_database`, `denied_store` (the credential denylist, counted but not a loss: they never make a sync `ok_with_omissions`), `unencodable_value` and `database_unreadable` (a generic database that failed to read; the rest of the sync continues, and it does make the sync `ok_with_omissions`).
- Generic record values are scrubbed before archiving: JWT-shaped strings, the values of `access_token`, `refresh_token`, `id_token`, `authorization`, `client_secret` and `password` keys, and `sig=` URL parameters become `[redacted]`. The sync report counts them as `redacted` (not an omission).
- Each sync clears the value of archived `records` rows whose database or store name the denylist now matches (the one case the archive removes content).

### Changed

- The archive schema version is 3. A version 2 archive gains the `records` table when it is next opened for writing, and its existing rows are untouched. The decoder version is 3, so the first sync after the upgrade re-reads an unchanged cache to fill `records`.
- Releases publish their Homebrew cask themselves. A stable tag's release workflow now pushes `m365crawl.rb` to the tap right after the published artifacts verify, instead of waiting for the tap to poll for it (the poll was scheduled hourly but really ran every three to nine hours).
- The database allowlist is replaced by a credential denylist: m365crawl mirrors everything Teams cached except sign-in credentials. A database or object store whose name starts with `Teams:auth`, contains `auth`, `token`, `credential`, `secret`, `cookie`, `msal`, `oneauth`, `key-store`, `keystore`, `keyval`, `session`, `key-value`, `keyring`, `crypto`, `encrypt`, `pkce`, `bearer`, `password`, `refresh` or `adal`, or has `aad`, `keys`, `jwt` or `e2ee` as a whole segment is never opened for values. The three typed databases' other object stores are mirrored too.
- An archive written by a newer schema version is refused with the coded `archive_newer` error (exit 3). Builds of v0.1.0 and earlier cannot do this: on a version 3 archive they report a generic `db_error`. Do not point an old binary at an upgraded archive.
- A sync reads the cache's LevelDB more than once now (once for the typed databases, then once to size the generic databases and once per batch), so a sync takes longer on a real cache. Real-cache timing, memory and record counts per manager are not measured yet; sync and watch cost on a large cache is gated on a run on a work machine before release.
- A sync is faster and allocates less: LevelDB table files stay open for the life of a reader instead of being opened for every block re-read, and the block cache is bounded at 4 MiB instead of 16 blocks. The rows a sync produces are unchanged and the decoder version stays 3.

## [0.1.0] - 2026-10-01

The first non-alpha release. Everything below is relative to 0.1.0-alpha.1. No JSON field was renamed or removed and the exit codes did not change, so scripts written against the alpha keep working, with the text changes noted under Changed.

### Added

- Implicit sync notice: a read that runs the `--max-age` sync first prints one line on stderr before it starts and never on stdout: plain in text mode (`m365crawl: syncing — archive is 2h14m old (max-age 15m)`, or `… no complete sync yet …`), one JSON line in json and log mode (`{"notice":"syncing","reason":"stale"|"never_synced","archive_age_seconds":N,"max_age_seconds":N}`), and its result gains `synced: {seconds, status}` beside `archive_age_seconds`. Reads of a fresh archive stay silent and carry no `synced`.
- `mention_kind` on message items that mention you (`person`, `channel`, `team`, `tag`, `everyone`, or `other` for a kind this version does not know), and `--direct-mentions` on `messages`, `search` and `activity` to keep only `person` mentions, so @channel and @team broadcasts do not drown "who mentioned me".
- `actor_id` and `actor_name` on activity items: who reacted, replied, mentioned or posted (a reaction's actor is a best-effort match by time and carries `actor_inferred: true`), distinct from `sender_*` (the related message's author). `msGraph`, `teamMembershipChange` and `threadActivity` have no actor and omit them.
- `teams`: lists teams `{team_id, display_name, channel_count, last_activity_at, unread_count}` (with `tenant_id`, `user_id`), honoring `--account`, `--limit` and `--fields`. The `--team` usage error now points at it.
- `unread --since <duration|time>` counts only unread messages sent at or after the cutoff, with `--by-conversation`, `--team` and `--include-channels` too: the recency cap for "what needs my attention", since old read markers leave conversations with hundreds of unread messages.
- `watch [--every 60s] [--emit-initial]`: streams one JSON line per new, edited or deleted message or activity item as Teams writes its cache (file events with a poll as the safety net, debounced), plus one `sync` line per sync and one `migrated` line when an upgrade re-derives an older archive. It honors `--account`, `--fields` and `--max-text`, skips system pseudo-conversations and exits 0 on SIGINT or SIGTERM.
- `--team <name|id>` on `conversations`, `messages`, `search`, `unread` and `activity`: limits results to one team and its channels. An unknown or ambiguous name is a `usage` error that lists the matches.
- `total` on truncated list results and on truncated `sql` results: the exact number of matches ignoring `--limit`. It is omitted when the result is not truncated.
- `reply_count` and `last_reply_at` on channel thread roots in `messages`, `search`, `unread` and `thread`.
- `search` works with filters alone (`--mentions-me`, `--from`, `--conversation`, `--team`, `--since`, `--until`). With nothing at all it is a `usage` error that points at `messages`.
- `activity --type` takes a comma-separated list, and `--help` and the skill document every activity type seen in a real cache.
- `conversations --query` ranks an exact name first, then a prefix, then a substring, then word matches.
- Archive upgrade: `meta.derivation_version` and the `migrated` report field (`{from, to, rows}`). See Changed.
- `skill`: prints the agent guide, embedded in the binary, as raw Markdown in every output mode (an exception to the JSON default, like `--help`), so the guide always matches the installed version.
- `version` prints `{"version", "commit", "date"}` as JSON (a human line in text mode), and `m365crawl --version` does the same.
- Partial syncs: each Teams source commits on its own. When one fails and another succeeds, `sync` reports status `partial` with a result per source (`accounts`, `counts` or `error`), prints the report on stdout and a new `partial_sync` error (exit 1) on stderr. `watch` emits the committed sources' changes, then an error line, and retries only the failed sources.
- `doctor` checks `archive_newer` (fails when a newer m365crawl wrote the archive), `archive_upgrade` (warns that the next sync upgrades an older archive) and `last_sync_status` (warns after a partial or failed sync).
- A second Ctrl-C (or SIGTERM) during a stop quits at once with exit 130.
- The release workflow runs `make check` before it publishes.
- Error codes `interrupted` (exit 1; a command stopped by SIGINT or SIGTERM, which alpha.1 reported as `internal`) and `archive_newer` (exit 3; an archive written by a newer m365crawl is refused for writing before anything is written, and read commands keep working).
- Real-cache acceptance tests (`make acceptance`, local only): a differential test of the native V8 decoder against Node's `v8.deserialize` for every allowlisted record, volume comparison against an independent Python reader, blob resolution, recency, and a check that the auth databases are never decoded.
- End-to-end tests that run the built binary against the committed fixture (`make e2e`), including the lock, interruption and environment error paths.
- A 100% per-function statement coverage gate over `internal/...` (`make coverage`, required in CI), with a shrinking per-package allow-list.
- Developer ID signing and notarization of the darwin binaries in the release pipeline, when the Apple signing secrets are available.
- `SPEC.md` (the normative specification), `docs/commands.md`, `docs/how-it-works.md`, `docs/full-disk-access.md` and this changelog.

### Changed

- Bot, card and call items read as text. A bot's Adaptive Card, hero or connector card gives its title, text blocks and facts as `text`; call and meeting events read `Call ended · 23m`; thread events read `Member added`; call recording and transcript notices read `Call transcript available` instead of raw JSON. `raw_json` is unchanged. Call events show a sender, taken from the people table when the message carries no name. A script that tested for empty `text` on these items will now see text.
- Untitled group chats are named after their other members (`Ana, Ben, Chao +2`), or `Unnamed chat (5 members, id 3fa9c2d1)` when no member name is known.
- An archive made by alpha.1 is upgraded in place. The first `sync` (or `watch`, or implicit sync) re-derives message text, sender names and untitled-chat names from each row's stored `raw_json`, including rows Teams has since evicted from its cache, rebuilds the text indexes and records derivation version 2. The rows count as no update and no edit. Read-only commands do not migrate. The sync can report `status: unchanged` and `migrated` together.
- Peak memory during a full sync fell from about 0.8 GB to about 0.2 GB on a real cache: the reader keeps values only for the allowlisted databases and re-reads values of 512 bytes or more from the snapshot on demand. The reader decodes LevelDB tables with its own block reader instead of goleveldb's.
- `sync` applies each source in one transaction in batches of 2,000 records, so memory stays bounded and a failed source rolls back completely.
- A relative `--db` path is resolved against the current directory.
- `archive_age_seconds` counts from the last fully successful sync of the accounts the read covers (`--account`, or the stalest account); a partial or failed sync refreshes nobody. `needs_sync` also appears when no complete sync exists yet, including an archive from an older version that has not been upgraded. In JSON mode the plain-text `hint:` line on stderr is gone (the result carries `hint`); text mode still prints it.
- `sql` streams rows and stops at `--limit`: `truncated` is still set, but `total` is no longer reported. A word such as `attach` inside a string literal, comment or quoted name no longer makes a query a usage error.
- `whoami`'s nested `archive.archive_age_seconds` equals the top-level one.

### Fixed

- A relative `--db` no longer fails with `invalid uri authority`.
- An interrupted command reports `interrupted` instead of `internal`.

### Security

- Documented that the archive holds message content verbatim, including anything people pasted (links with tokens included), and that the archive and its lock file are mode 0600 in a 0700 directory (SPEC.md section 8).

## [0.1.0-alpha.1] - 2026-09-30

First public alpha.

### Added

- `doctor`, `whoami`, `sync`, `status`, `search`, `messages`, `thread`, `unread`, `activity`, `conversations`, `people` and a read-only `sql`.
- A native reader for the new Teams desktop cache: a manifest-driven LevelDB reader that does not depend on the IndexedDB key comparator, Chromium's IndexedDB coding, the Blink value envelope (snappy, blobs, the version 21 trailer) and a Go V8 structured-clone deserializer for wire versions 13 to 16. No Node, Python or browser is needed at runtime.
- A SQLite archive (WAL) with FTS5 search, idempotent upserts, sticky `deleted_at` and a keep-forever rule for messages Teams evicts from its cache. Only the `replychain-manager`, `conversation-manager` and `activity-manager` databases are decoded; the auth database never is.
- An agent-first JSON contract: one document per command on stdout, stable snake_case keys, lists as `{items, count, truncated}`, errors as `{error: {code, message, fix}}` on stderr, exit codes 0 to 4, `archive_age_seconds` on every read result, `--max-age` implicit sync with a `sync_error` fallback, `--fields`, `--max-text`, and a Teams deep `link` on every item.
- Unread state (`unread`, `--by-conversation`, `--include-channels`, `channels_excluded`) and exact `mentions_me`.
- A terminal presentation for text mode, a README that argues the tool, the agent skill, a Homebrew cask, and release builds for darwin amd64 and arm64.

### Known issues

- Peak memory of about 1 GB during a full sync.
- Bot, call and some activity items could have an empty sender or empty text, or raw card JSON as text.
- No reply counts on thread roots, no `--team` filter and no `watch`.
- Unsigned binaries.

[0.1.0]: https://github.com/ourostack/m365crawl/compare/v0.1.0-alpha.1...v0.1.0
[0.1.0-alpha.1]: https://github.com/ourostack/m365crawl/releases/tag/v0.1.0-alpha.1
