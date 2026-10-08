# Privacy

m365crawl reads the Teams and Outlook caches already on your computer and writes one private archive on the same computer. Nothing leaves the machine. Every command is offline except `transcripts fetch` and `transcripts signin`, which reach SharePoint only through a browser m365crawl starts with its own profile; m365crawl never sees a password or token. Everything else comes from files Teams and Outlook already keep on this machine. The archive holds your real chats, mail and meetings, so protect it like the mailbox it contains. [SPEC.md](../SPEC.md) section 8 is the normative version of this page.

## What m365crawl reads

| Source | Where | How |
| --- | --- | --- |
| Teams chats, channels, activity feed and calendar | The new Teams app's cache (`EBWebView`; see [commands.md](commands.md#platform-defaults) for the path) | Copied to a private temporary directory and read from the copy. |
| Outlook mail and calendar (macOS) | The new Outlook for Mac's `HxStore.hxd`, one per profile | Copied to a private temporary file and read from the copy. |
| Outlook message bodies (macOS) | Body files under the Outlook profile's `Files` directory | Opened read-only, only for messages the sync reads, only below `Files`. |

On macOS reading these needs Full Disk Access for the app that runs m365crawl ([full-disk-access.md](full-disk-access.md)). On Windows m365crawl reads only the Teams cache; Outlook is not read there yet.

## What m365crawl never does

- **It never writes to Teams or Outlook.** There is no code path that writes to either app's storage. It cannot send, reply, react, move, delete or mark anything read.
- **It never reads sign-in credentials.** Teams databases and object stores whose names look like credentials (`auth`, `token`, `credential`, `secret`, `cookie`, `msal`, `oneauth`, `key-store`, `keystore`, `keyval`, `session`, ignoring case) are never opened for values; a sync reports only how many it skipped. Token-shaped values found in other records (JWTs, `Bearer` strings, `access_token` and similar keys, `sig=` URL parameters) are replaced by `[redacted]` before they are archived, and the sync report counts them in `redacted`.
- **It uses the network only to fetch meeting transcripts.** Every command is offline except `transcripts fetch` and `transcripts signin`, which reach SharePoint only through a browser m365crawl starts with its own profile; m365crawl never sees a password or token. The sign-in state lives in that browser profile (`browser` beside the archive, readable by you only), and m365crawl never reads it. Every other command reads files Teams and Outlook already keep on this machine. There is no app registration, no API token and no telemetry. Mail HTML is converted to text without fetching images or any other remote content.
- **It never copies attachment files.** Attachments are recorded as metadata only: name, size, content type and whether Outlook's cache holds the file.

## What the archive holds

The archive is one SQLite file. It holds content verbatim, including anything people pasted into a message or mail: links with tokens, credentials, file names, customer data.

- **Teams:** messages with their text and HTML, conversations, people, the activity feed, unread state, and every other non-credential Teams database as raw records. `raw_json` keeps each full original record.
- **Mail:** each message's subject, sender, recipients (names and addresses), times, folder, read and flag state, Message-ID, the body as text, and the body HTML, compressed, so the text can be derived again later. Attachment names are stored too, and they can be sensitive.
- **Calendar:** events from Teams and Outlook with attendees, bodies, recaps and action items.
- **Signed-in addresses:** to link an Outlook profile to your Teams account, the archive keeps the addresses signed in to each profile, lower case, in its `meta` table. They are never printed, logged or put in an error or a report.

Mail and messages that leave the apps' caches stay in the archive, marked `evicted` or `gone`, until you delete the archive.

## Where the archive lives and who can read it

- **macOS:** `~/.m365crawl/m365crawl.db`. The directory is mode 0700 and the archive and its lock file are mode 0600. `m365crawl doctor` warns (`mail_archive_mode`) if other users can read the archive file.
- **Windows:** `%LOCALAPPDATA%\m365crawl\m365crawl.db`. The directory grants access only to your user and SYSTEM. A custom `--db` must sit in a directory that is already private, or one m365crawl creates; otherwise m365crawl refuses to open it (`db_error`) before writing anything.

Treat the archive like the mailbox it contains: do not commit it, sync it to shared storage, or paste its contents into tools you would not show the original messages to. When you hand m365crawl to an agent, the agent can read everything in the archive.

## Temporary copies

The copy of the Teams cache includes Teams' sign-in database, which m365crawl never decodes. Each copy lives in a private temporary directory (mode 0700 on macOS, your user and SYSTEM only on Windows) for the length of one source's read, and is deleted on success, on failure and on Ctrl-C or SIGTERM. A sync also removes leftover copies older than an hour, for example from a killed process. The Outlook store copy follows the same rules.

## Tests and documentation

The repository's tests, fixtures and documentation use synthetic data only. Checks against a real cache run on the developer's own machine and record counts, timings and pass or fail, never content.

## Deleting your data

Delete the archive directory (`~/.m365crawl` on macOS, `%LOCALAPPDATA%\m365crawl` on Windows), or the file you passed with `--db`. That removes everything m365crawl stored. [install.md](install.md#removing-m365crawl) covers removing the program too.
