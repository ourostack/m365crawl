### Added

- The overview (bare `m365crawl`) reports documents, notes, people and transcripts beside chats, mail and the calendar, with counts, registered and failed sources, and the last capture or fetch, all from what the archive already holds. Documents and notes with nothing registered have state `not_registered` and name the sync command. "Start here" gains `docs list`, `notes search` and `people`.
- An unscoped `people` list carries a notice, in text and JSON, saying it included Teams people and mail correspondents of every account and, when people sources are registered, native Outlook observations whose account ownership is unresolved.
- `people --help` explains that `last_seen_at` is the newest time a person appears in archived data, not the last chat with you, points to `conversations` and `messages --conversation` for chat recency, names the ID kinds (`8:orgid:` people, `28:` bots and apps, `19:` chats and teams named in mentions, `everyone`, `mail:`), and says `--account` takes the `<tenant_id>/<user_id>` that `whoami` lists.

### Changed

- Text output prints every time with its zone name (`2026-10-12 09:30 PDT`), so a text day is not mistaken for the UTC day JSON prints.
