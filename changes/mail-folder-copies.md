### Fixed

- Mail on Mac no longer lists or counts a message twice when Outlook has rebuilt the account with a new set of folders: a message that a newer folder set also holds (same Message-ID, same kind of folder) is listed and counted once, from the newer copy, in `mail list`, `mail unread`, mail `search`, `mail thread` and the `mail folders` counts. Nothing is deleted and ids do not change; `mail list --include-copies` lists the older copies too.
- A trusted mail read no longer clears the coverage of a folder from an older folder set it does not read, and the gone/evicted rule judges against the read's own folder set only.
- The empty-result note and the "cache covers since" footer of the mail lists measure from the current folder copies, and the note says when only an older copy of the folder reaches the requested date.

### Added

- Mail message outputs add `older_copy` and `copy_of`; `coverage[]`, `mail folders` items and `mail unread` folders add `current`, which is `false` for a folder of an older folder set.
- `mail list --since/--until` adds `folder_copies`, which compares the current copy of each selected folder kind with its older copies over the range and reports `partial` when an older copy holds messages dated inside the current cache's range that the current cache lacks, with a notice.
