### Added

- More directed writes through WorkIQ (part of #250): `writes send-mail` (new mail), `writes reply-all-mail`, `writes save-mail-draft --draft ID` (update a draft), `writes discard-mail-draft`, and `writes add-reaction` / `writes remove-reaction` (an emoji on a chat message). Mail goes through a draft that is read back and checked before it is sent and counts as sent once found in Sent Items; draft updates and discards, and reactions, count as done once a read shows them. `writes target` gains `--chat-message`, `--all` and `--draft`. The archive (schema 30) records each write's reaction.

### Changed

- The directed actions contract v0 gains `reply_all_mail`, and its outcome-unknown rule treats a reply and a reply-all to the same message with the same text as the same request. A tool without the new action answers `unknown_action`, which refuses safely. A discard whose fresh read shows the message is no longer a draft now ends `failed` without acting.
