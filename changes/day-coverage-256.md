### Added

- `m365crawl coverage` says, per day and source (Sent Items, the Inbox, chats, meeting chats, channels and the calendar), whether the archive holds everything the app cached, so a real zero can be told apart from a day the archive does not cover. Each entry has `state` (`covered`, `partial`, `not_covered`), an exact `items` count, the covered part, `reasons`, a `fix` and WorkIQ `next` steps where they help. `--day`, `--since`/`--until` (at most 31 days), `--source` and `--tz`.
- A `mail list` or `messages` read bounded by `--since` or `--until` adds `range_coverage`, the same verdict over the read's range, independent of `--limit`, `--fields` and the other filters, also with zero hits.
- Each mail read that is complete enough records, per account, the time spans in which Outlook's cache held every Sent Items and Inbox message, so coverage survives Outlook evicting older mail. No schema change: the spans live in the `meta` table.

### Changed

- `workiq_routes` also routes a range the read's `range_coverage` does not cover for a reason WorkIQ can help with (new reason `range_not_covered`), including a hole inside the cached range, not only a range that starts before the oldest cached item.
