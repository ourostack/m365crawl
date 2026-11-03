### Fixed

- Writable archive opens share a read-only connection for their preflight integrity checks, avoiding repeated SQLite schema setup within the unchanged short-writer acquisition deadline.
