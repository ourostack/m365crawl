### Fixed

- Mail on Mac no longer drops a message from `--folder sent` (or any folder) because another copy of it was changed more recently: a message sent to yourself is in Sent Items, Inbox and To Me, and a sent message whose Inbox copy you deleted is in Sent Items and Deleted Items. Each sync now records every folder a message has a copy in, `--folder` matches any of them, and `mail folders` counts the message in each, unread only where that copy is unread. `folder` and `folder_kind` still name the copy the message is shown from, and ids do not change.
- A message with a copy in Sent Items is dated by its send time in every list, not only when it is shown from Sent Items.
- An older folder set's Sent Items message now counts as held by the current set when the current set has it in Sent Items through any copy, so it is no longer listed twice or reported as missing from the current copy.
- `mail unread`'s `unread_total` counts each unread message once, even when it is unread in several folders. Without `--folder`, `--unread` and the status mail block count a message as unread when any of its copies is.

### Added

- Mail message outputs (`mail list`, `mail show`, `mail thread`, `mail unread` and mail `search` items) add `folders`, every folder the message is in, each with the read state of the copy there. The item's own `is_read` stays the state of the copy it is shown from.
- Archive schema 23 adds `mail_message_folders`. An older archive gains the table, empty, on its next writable open, and the next mail sync fills it.
