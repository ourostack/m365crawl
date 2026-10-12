### Added

- `docs list` and `docs search` take `--application` (Word, Excel, PowerPoint, or supplied for `--root` files) and `--surface` (recent, shared, recommended, dialog); other values are usage errors that name the accepted ones. Both narrow the rows only; `source_coverage` still lists every source.
- `docs --help`, `docs list --help` and `docs show` define the four surfaces, `association_state` and `boundary` `unresolved`, the native `Sharing.Type` number, and the `modified_at`, `captured_at` and `source_observed_at` clocks, and state the list order and what a null `modified_at` means.

### Changed

- **Breaking:** `source_id` in `docs sources`, `notes sources` and `people sources` rows (and in each command's `source_coverage`) is now null, not 0, for a locator that has never had a capture admitted. A consumer that reads it as a number must accept null; `kind` and `locator_tail` identify such a row. Observation rows keep an integer `source_id`.

### Fixed

- `--format log` no longer fails with `invalid log label` for subcommands such as `docs list`: the log key is the command path with `_` for spaces (`docs_list={...}`).
- A failed document source is now identifiable and actionable: every source row carries `locator_tail`, text tables show `kind` and the path tail, and failed or cancelled rows carry `fix`, the remedy for their reason, and `effect`, which says whether the last good capture stays current. Every remedy that retries names plain `m365crawl docs sync`, which also reaches a source whose `source_id` is null. `notes sources` and `people sources` rows gain `kind` and `locator_tail`.
