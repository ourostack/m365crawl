### Added

- `docs sync` reads the Office registry's recently-opened list: the Mac default registry files, or one ordinary local copy given with `docs sync --registry-file FILE` on Mac or Windows. Each entry is a document observation with `variant` `office_registry`, application `registry`, the registry's own claims in `registry_native` and its opened time in `opened_at` (null when the registry gives none or it is not an RFC 3339 time with an offset; the raw text is kept). The registry states no modification time, so `modified_at` is null and `--since`/`--until` never match these rows. Nothing the registry refers to is opened, checked or resolved.
- `docs list` and `docs search` accept `--application registry`. Shared `search` returns registry rows with the other document metadata.
- A default `docs sync` tries the Office JSON caches and the Office registry independently and reports a family that is absent or refused under `discovery`, with a fixed code and the supplied-input command to use instead.
- A registry source whose first capture failed has no source ID, so its `fix` names the retry `m365crawl docs sync --registry-file FILE` with the file's path, quoted so that pasting it cannot run text from the file name. A failed source stays listed until a later sync of it succeeds; there is no command to remove one.
- The archive moves to schema 14: additive `variant`, `registry_json` and `opened_at` columns for document observations. Existing rows are not rewritten.

### Changed

- **Breaking:** document `source_coverage.scope` is renamed from `office_json_metadata` to `office_document_metadata`, because it now covers Office JSON and Office registry sources. The `metadata` manifest's privacy scopes are a separate list: it keeps `office_json_metadata` and adds `office_registry_metadata`.
- **Breaking:** a default `docs sync` whose Office JSON caches are missing or refused exits 0 when a registered source was attempted or the Office registry was usable, and reports the refusal under `discovery`; before, it failed with the refusal's code and exit 3 without trying the registered sources. When neither family is usable and no source is registered it still fails with that code and exit 3. `docs sync --root DIR` still fails on a refused directory.
- `--root`, `--registry-file` and `--source-id` of `docs sync` are mutually exclusive. A `--registry-file` that does not exist, is not a regular file, or is the archive database is a usage error and registers nothing.
- Text `docs list` and `docs search` have an `opened_at` column for registry rows; it is `-` for Office JSON rows.

### Fixed

- Reading OneNote indexes and Office files in one process can no longer release a lock another capture still holds: one gate now serializes every open and close of those source files.
