### Fixed

- Reads run in parallel no longer fail with `locked`. When a read's implicit sync finds another m365crawl run holding the archive lock, it waits for that run (up to 2 minutes) and answers from its sync without syncing again (`implicit_sync: "waited_for_other_sync"`); if the wait runs out it answers from the archive as it is, exits 0 and says so (`implicit_sync: "other_sync_in_progress"` and a notice) instead of reporting `sync_error: locked`. Notes, documents, Loop and people refreshes inside a read wait the same way. Explicit `sync` still exits 4 `locked`.
