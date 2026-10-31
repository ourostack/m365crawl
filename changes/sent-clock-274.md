### Fixed

- `mail list --since/--until`, mail `search` and the folder-copy check date Sent Items by when the message was sent (`sent_at`), not by the received time Outlook stores on it, which is often when the draft was started or a time on the next day: a message sent late in the evening now lists on the day it was sent. Other folders, Drafts included, stay on the received time. Lists sort newest first on the same clock, and `mail thread` oldest first, so a sent reply whose stored received time is the draft's start no longer appears above the messages it answers.
- When copies of a message in different Outlook folder sets disagree on received time by more than a minute, the message is dated by the earliest copy's received time at or after its send time (15 minutes of clock skew allowed), instead of by whichever copy the folder-set ranking picked; with no such copy, the listed row's own time stays.

### Added

- Mail message outputs add `dated_at` (the time a message is listed, filtered and sorted by) and `received_at_disagrees` (copies in other folder sets hold received times more than a minute apart); `folder_copies` entries add `dated_by`. Text mail tables show `dated_at` in a `date` column.
