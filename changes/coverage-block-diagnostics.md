### Changed
- Report exact zero-count statement ranges from failing source files when the Unix function-coverage gate fails, without changing coverage thresholds or allowlists.

### Fixed
- Make the OneDrive ListSync scope-loop cancellation regression deterministic despite background database context checks, without changing the reader.
