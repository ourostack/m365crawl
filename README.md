# teamscrawl

teamscrawl mirrors the Microsoft Teams desktop cache on macOS into a local SQLite archive that AI agents can search and read. It works offline and read-only: it copies a snapshot of the cache, never writes to Teams' storage, and never reads Teams tokens.

## Status

Early scaffold. Only `teamscrawl version` exists today. The sync, search and messages commands are being built.

## License

MIT
