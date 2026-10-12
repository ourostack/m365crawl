### Added

- `docs list` and `docs search` take `--application` (Word, Excel, PowerPoint, or supplied for `--root` files) and `--surface` (recent, shared, recommended, dialog); other values are usage errors that name the accepted ones.
- `docs --help`, `docs list --help` and `docs show` define the four surfaces, `association_state` and `boundary` `unresolved`, the native `Sharing.Type` number, and the `modified_at`, `captured_at` and `source_observed_at` clocks, and state the list order and what a null `modified_at` means.

### Fixed

- `--format log` no longer fails with `invalid log label` for subcommands such as `docs list`: the log key is the command path with `_` for spaces (`docs_list={...}`).
- A failed document source is now identifiable and actionable: `source_id` is null (not 0) until a capture is admitted, every source row carries `locator_tail`, text tables show `kind` and the path tail, and failed or cancelled rows carry `fix`, the remedy for their reason, and `effect`, which says whether the last good capture stays current. `notes sources` and `people sources` rows gain `kind` and `locator_tail` and the same null `source_id`.
