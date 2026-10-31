# Privacy

`drafts list/show` can expose private, never-communicated compose material from
the selected archive. They do not sync, discover, register, migrate, start a
browser, use network/authentication, send, edit or discard. Draft content is
excluded from shared messages/search/thread/unread/activity/FTS. Raw HTML is data,
not executed or hydrated; assets/cards/links are not fetched. Do not quote compose
material as something already communicated. Unknown ownership/representations
are withheld; absence and removal are not server lifecycle facts. Body guards
exclude freshly read metadata/target evidence and do not create immutable history.

Native `calendar attendees sync` reads only a guarded private HxStore copy.
Calendar, mail and eligible registered optional lanes share that copy; all readers
close before one verified cleanup and optional admission. Standalone attendee
capture never reads or erases mail. `calendar attendees sources/list/show` are
strictly archive-only, including expired max-age: no native path inspection,
registration, copy, browser, credentials, network, migration or repair.
The archive retains literal nullable native names/emails, physical geometry,
canonical source paths, exact requests, input/representation digests, original
clocks, losses and read/link membership. These can be private even when ambiguous
or unlinked; they do not resolve a person or establish current attendance.
Never include real source/archive contents in examples, logs, fixtures or issues.

Explicit `content fetch engage-home` is the additional fixed-root Engage browser
workflow. It retains bounded partial native thread-starter sessions, not a feed,
and never reads credentials, copies an ordinary browser profile or infers a
Teams/person identity. Unknown attempts are registration diagnostics, not an
account diagnosis. Agreed native sign-in discards content and registers nothing.
Ordinary reads, including expired max-age, remain archive-only. Protect retained
Engage bodies like other private messages; never put them in fixtures/issues.
See [Engage scope and platform limitations](engage-home.md).

m365crawl reads the Teams and Outlook caches and writes one private archive on the same computer. Explicit `transcripts fetch`, `transcripts signin`, `content fetch page/stream` and `content signin URL` reach their selected cloud targets through a tool-owned browser profile. Optional explicit `workiq sync/coverage/status` instead use the user's already-installed WorkIQ subprocess; its sign-in stays outside m365crawl. Ordinary reads never start WorkIQ, and ordinary content reads remain archive-only without fetch or sign-in. Protect the archive like the mailbox it contains. [SPEC.md](../SPEC.md) section 8 and its optional-source contract are normative.

## What m365crawl reads

| Source | Where | How |
| --- | --- | --- |
| Teams chats, channels, activity feed and calendar | The new Teams app's cache (`EBWebView`; see [commands.md](commands.md#platform-defaults) for the path) | Copied to a private temporary directory and read from the copy. |
| Outlook mail and calendar (macOS) | The new Outlook for Mac's `HxStore.hxd`, one per profile | Copied to a private temporary file and read from the copy. |
| Native attendee evidence | Explicit Mac HxStore discovery or supported copied profiles | Explicit sync or an already-required ordinary copy for registered lanes; native Windows/OWA acquisition remains unqualified, offline archive reads are portable. |
| Outlook message bodies (macOS) | Body files under the Outlook profile's `Files` directory | Opened read-only, only for messages the sync reads, only below `Files`. |
| Outlook mail and calendar (Windows) | New Outlook's OWA cache under `%LOCALAPPDATA%\Microsoft\Olk\EBWebView`, one IndexedDB origin per profile | Read from a validated private snapshot with separate allowlisted mail and calendar stores; never credential stores or attachment payloads. Calendar and mail health are independent; no calendar deletions or unverified account links are inferred. |
| Explicit WorkIQ document excerpts | One OneDrive/SharePoint Office file link selected by the caller | `content fetch doc` only, through the user's signed-in WorkIQ subprocess. Text is kept only when WorkIQ reports `contentProtection: "allowed"` (the EXTRACT right) for that file; only the excerpts WorkIQ released, with the label and rights check. File bytes are inspected for encryption when no encrypting label accounts for it, then discarded, never decrypted. A later refusal deletes the kept text. The link is never stored. |
| Explicit SharePoint page / Stream evidence | One supported target URL selected by the caller | Acquired only by `content fetch` through the tool-owned headless profile; supported projection/selected body, not a crawl or rendered export. Native Windows acquisition remains unqualified. |
| Microsoft Scout session history (opt-in) | The session files Scout keeps in your local OneDrive folder (`Apps/<Scout folder>/sessions/devices/<device>/<session>.json`, on macOS or Windows), or a folder named with `--root` | Read only by explicit `scout sync`, only files already on this computer. On macOS each read runs with downloads of cloud-only files turned off; on Windows a file is opened only when its folder listing shows it is on this computer, asking Windows not to download. A cloud-only file is counted and never opened. |
| Explicit resident Office text | One absolute ordinary local DOCX/XLSX/PPTX file, or selected original native local file URL | `content capture` only, under positive no-hydration/no-recall native guards; production parser reads a bound private copy. No Office launch, network, discovery or implicit capture. |

On macOS reading these needs Full Disk Access for the app that runs m365crawl ([full-disk-access.md](full-disk-access.md)). On Windows m365crawl reads the Teams cache and new Outlook's cached mail, and needs no extra permission.

## What m365crawl never does

- **It never writes to Teams' or Outlook's storage.** There is no code path that writes to either app's storage. By default it cannot send, reply, react, move, delete or mark anything read.
- **Writes happen only when the person turns them on.** `m365crawl writes on`, typed by the person on their terminal, lets agents they direct send a Teams chat message or save a new mail draft as them, through WorkIQ. The switch is a file beside the archive. Each write is recorded in the archive with the agent, its reason, the acting account, the recipients and the text, and is sent only after m365crawl checks that the account, the chat's audience and its version still match. This guards against accidental writes; an agent with shell access as the user could still call WorkIQ directly.
- **It never reads sign-in credentials.** Teams databases and object stores whose names look like credentials (`auth`, `token`, `credential`, `secret`, `cookie`, `msal`, `oneauth`, `key-store`, `keystore`, `keyval`, `session`, ignoring case) are never opened for values; a sync reports only how many it skipped. Token-shaped values found in other records (JWTs, `Bearer` strings, `access_token` and similar keys, `sig=` URL parameters) are replaced by `[redacted]` before they are archived, and the sync report counts them in `redacted`. When a later version denies a name, or scrubs a key, that older syncs archived, the next sync clears those values from the archive, and for a key that held a credential, the key too: the row takes the scrubbed key, or is deleted when another row already has it. m365crawl writes the archive with SQLite's `secure_delete` on, so anything it replaces, clears or deletes is overwritten with zeros in the database file instead of being left in free space, and after a purge it empties the archive's write-ahead log. If another program is reading the archive and keeps the log from being emptied, the sync report says `purged_log_pending` and the next sync tries again. An archive an older version wrote is rewritten once (a SQLite `VACUUM`) by the next `m365crawl sync` or `watch`, so nothing that version left in free space remains in the file. It needs twice the archive's size free on its disk; until then the sync report and `m365crawl doctor` say `vacuum_pending` and how much space it needs (SPEC.md section 3.2).
- **It uses the network only through explicit delegates.** `transcripts fetch`, `transcripts signin`, `content fetch page/stream` and `content signin URL` use a tool-owned browser profile (`browser` beside the archive), never a copied everyday profile. WorkIQ uses only the user's subprocess and an MCP transport limited to `fetch`, plus fixed `retrieve` and `fetch_blob` calls for one explicit `content fetch doc` file and, with writes on, one fixed `create_entity` call per directed write, never an HTTP/token client, sign-in or agreement automation. Disabled WorkIQ does nothing except an explicitly requested status probe; enabled doctor runs version only. No client capabilities, filesystem roots or interactive prompts are granted to the MCP server. Mail HTML is converted to text without fetching remote images or attachments.
- **Server JSON is scrubbed before retention.** Whole `@microsoft.graph.downloadUrl` values and `sig=`/`tempauth=` query values redact along with existing credentials. This cache-wide rule update causes one full desktop reread. A cursor that would redact rejects the entire page, rather than storing a damaged cursor. Raw child output, private account addresses and server bodies are never echoed in errors or reports. A failed WorkIQ run's `diagnostic` keeps only the process's exit status, a failed server result's HTTP status and Graph error code, and short output tails that pass an allowlist of common log words, error class names, HTTP status numbers that follow `status`, `http` or `code`, and hashed request IDs; every other word becomes `[redacted]`. WorkIQ sync retains scrubbed server content and opaque safe cursors; coverage/status discard payloads and retain only their enabled-source identity/outcome/count metadata. Disabled status writes none.
- **It never copies attachment files.** Attachments are recorded as metadata only: name, size, content type and whether Outlook's cache holds the file.

## What the archive holds

The archive is one SQLite file. It holds content verbatim, including anything people pasted into a message or mail: links with tokens, credentials, file names, customer data.

- **Teams:** messages with their text and HTML, conversations, people, the activity feed, unread state, and every other non-credential Teams database as raw records. `raw_json` keeps each full original record.
- **Mail:** each message's subject, sender, recipients (names and addresses), times, folder, read and flag state, Message-ID, the body as text, and the body HTML, compressed, so the text can be derived again later. Attachment names are stored too, and they can be sensitive.
- **Calendar:** events from Teams and Outlook with attendees, bodies, recaps and action items.
- **Native attendee evidence:** immutable ordered physical observations and separate selected-detail bindings, own complete ambiguity and original losses/clocks, per-attempt source/read/origin/link/census evidence. This is not a resolved directory or current roster.
- **Explicit cloud content:** verified native file/account metadata, supported page controls or one selected Stream transcript; retained bodies keep their original identity, qualification and clocks. No sharing tickets or temporary download URLs are retained.
- **Explicit resident Office text:** exact native snapshot/clock evidence, source-byte and representation digests, supported paragraphs/cached cell values, partial/loss counts and per-attempt selected reference claims. Original file bytes are not archived. Ordinary content reads stay archive-only, never inspect source paths or hydrate them. Treat retained text as sensitively as the selected original file.
- **Microsoft Scout session history (opt-in):** each session file's JSON as read (after secrets are replaced by `[redacted]` under the Teams rules and the Scout rules for tool output listed in [scout.md](scout.md#privacy); other secrets are kept as written, and archived sessions, gone ones included, are scrubbed again from the archive by the next `sync` or `scout sync` when the rules change, overwriting the replaced text), its messages (role, time, text and counts of tool calls and attached file names; reasoning text is kept only inside the stored JSON), the file's size, modification time and SHA-256 digest, and the folder's last two path elements in results. This is private AI conversation history: everything the agent saw and did, which can quote your mail, chats, files and tool output. Treat it like your private chats. Sessions stay in the archive after Scout or you delete them.
- **Signed-in addresses:** to link an Outlook profile to your Teams account, the archive keeps the addresses signed in to each profile, lower case, in its `meta` table. They are never printed, logged or put in an error or a report.

Mail and messages that leave the apps' caches stay in the archive, marked `evicted` or `gone`, until you delete the archive.

## Where the archive lives and who can read it

- **macOS:** `~/.m365crawl/m365crawl.db`. The directory is mode 0700 and the archive and its lock file are mode 0600. `m365crawl doctor` warns (`mail_archive_mode`) if other users can read the archive file.
- **Windows:** `%LOCALAPPDATA%\m365crawl\m365crawl.db`. The directory grants access only to your user and SYSTEM. A custom `--db` must sit in a directory that is already private, or one m365crawl creates; otherwise m365crawl refuses to open it (`db_error`) before writing anything.

Treat the archive like the mailbox it contains: do not commit it, sync it to shared storage, or paste its contents into tools you would not show the original messages to. When you hand m365crawl to an agent, the agent can read everything in the archive.

## Temporary copies

The copy of the Teams cache includes Teams' sign-in database, which m365crawl never decodes. Each copy lives in a private temporary directory (mode 0700 on macOS, your user and SYSTEM only on Windows) for the length of one source's read, and is deleted on success, on failure and on Ctrl-C or SIGTERM. A sync also removes leftover copies older than an hour, for example from a killed process. The Outlook store copy follows the same rules.

## Tests and documentation

The repository's tests, fixtures and documentation examples use synthetic data only. The one exception is the README's `m365crawl doctor` screenshot: a real run, scrubbed so that it shows only checks and archive counts, never names, addresses or content. Checks against a real cache run on the developer's own machine and record counts, timings and pass or fail, never content.

## Deleting your data

Delete the archive directory (`~/.m365crawl` on macOS, `%LOCALAPPDATA%\m365crawl` on Windows), or the file you passed with `--db`. That removes everything m365crawl stored. [install.md](install.md#removing-m365crawl) covers removing the program too.
