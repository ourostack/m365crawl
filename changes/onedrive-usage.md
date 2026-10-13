### Added

- `docs sync` reads OneDrive's local usage file: the Mac default files of each Business account folder, or one ordinary local copy given with `docs sync --onedrive-usage-file FILE` on Mac or Windows. Each recently used file, each formatted variant of it and each frequent-collaborator profile is a document observation with `variant` `onedrive_usage`, application `onedrive` and the file's own record in `usage_native` (`Type` `recent`, `document` or `collaborator`). One native ID can have a recent row and several document rows; none is preferred or merged. Nothing the file refers to is opened.
- A usage document row carries the sharing claims OneDrive recorded. `History` previews the first 100 sharing instances and each instance its first 100 participants, with the full captured counts, the number the file itself states and explicit truncation flags. `full_inspection` is an archive-only `sql` command that prints the row's complete stored record.
- Usage clocks are `{Raw, Value}`: the file's exact text, and UTC or null. `modified_at` is the file modification time only, so `--since`/`--until` never match an access, sharing or capture time.
- `docs show` of a usage row adds `usage_relations`: the rows of the same capture with the same native ID.
- `docs list` and `docs search` accept `--application onedrive`. Shared `search` returns usage rows with the other document metadata.
- A default `docs sync` tries the OneDrive usage files independently of the Office JSON caches and the Office registry. A `discovery` entry can now carry a `locator` when it is about one default location, such as an account folder whose name is not read.
- A usage source whose first capture failed has no source ID, so its `fix` names the retry `m365crawl docs sync --onedrive-usage-file FILE` with the file's path, quoted so that pasting it cannot run text from the file name.
- The archive moves to schema 15: additive `usage_json` and `native_key` columns and a `document_usage_relations` table. Existing rows are not rewritten, and reading a schema 14 archive does not migrate it.
- The `metadata` manifest's privacy scopes add `onedrive_usage_metadata`.
- Limit: OneDrive keeps a write-ahead log beside the usage file while it runs. A file with one is refused with `onedrive_usage_input_unqualified` and never consolidated, so the default Mac files are usually readable only after OneDrive has quit.
- Limit: only the usage file is read. OneDrive's library and sync databases are not read yet, there is no default discovery on Windows or Linux, and only Business account folders are read by default.
- Limit: names, addresses, thread IDs and collaborator profiles in a usage row are the file's claims. They are not linked to archived people, chats, mail or accounts, and no command follows them.

### Changed

- **Breaking:** a default `docs sync` on a machine with no usable document input now fails only when all three families (Office JSON, Office registry, OneDrive usage) are unusable and no source is registered; its message and fix name all three. On Mac it can therefore exit 0 where it failed before, when only OneDrive usage files were found.
- `--root`, `--registry-file`, `--onedrive-usage-file` and `--source-id` of `docs sync` are mutually exclusive. A `--onedrive-usage-file` that does not exist, is not a regular file, or is the archive database is a usage error and registers nothing.
- `docs list` and `docs search` break ties between rows with equal modification time, title and source by a usage row's native type, ID and format before the observation ID. The order of Office JSON and registry rows is unchanged.
