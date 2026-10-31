### Fixed

- Opening the archive for a sync (including each `watch` tick) now waits up to five seconds for another program that holds the archive's SQLite write lock, as the archive's busy timeout intends, instead of failing at once with `db_error` "database is locked".
