### Security

- The Teams credential purge (rows under a name the denylist now matches, or under a key that now scrubs differently) no longer leaves the cleared values on disk. It runs before the sync's removal marks, with SQLite's `secure_delete` on, so the values are overwritten with zeros, and the sync then truncates the archive's write-ahead log. When another program reading the archive blocks that, the sync report sets `purged_log_pending` and the next sync tries again. Copies that older syncs left in free space are overwritten only when SQLite reuses the space (#275).
