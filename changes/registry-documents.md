### Added

- `docs sync` reads the Office registry's recently-opened list: the Mac default registry files, or one ordinary local copy given with `docs sync --registry-file FILE` on Mac or Windows. Each entry is a document observation with `variant` `office_registry`, application `registry`, the registry's own claims in `registry_native` and its opened time in `opened_at` (null when the registry gives none or it is not an RFC 3339 time with an offset; the raw text is kept). The registry states no modification time, so `modified_at` is null and `--since`/`--until` never match these rows. Nothing the registry refers to is opened, checked or resolved.
- `docs list` and `docs search` accept `--application registry`. Shared `search` returns registry rows with the other document metadata.
- A default `docs sync` tries the Office JSON caches and the Office registry independently and reports a family that is absent or refused under `discovery`, with a fixed code and the supplied-input command to use instead.
- A registry source whose first capture failed has no source ID, so its `fix` names the retry `m365crawl docs sync --registry-file FILE` with the file's path.
- The archive moves to schema 14: additive `variant`, `registry_json` and `opened_at` columns for document observations. Existing rows are not rewritten.

### Changed

- A default `docs sync` on a machine where the Office JSON caches are missing or refused no longer fails the whole command: the refusal is a `discovery` outcome, and registered sources and the registry are still tried. `docs sync --root DIR` still fails on a refused directory.
- Document `source_coverage.scope` is `office_document_metadata` (was `office_json_metadata`), because it now covers Office JSON and Office registry sources. `metadata` lists the new local-only scope `office_registry_metadata`.
- `--root`, `--registry-file` and `--source-id` of `docs sync` are mutually exclusive.

### Fixed

- Reading OneNote indexes and Office files in one process can no longer release a lock another capture still holds: one gate now serializes every open and close of those source files.
