### Changed

- Shared `search` now includes admitted `content fetch doc` documents among its content hits (`source: content`), after other content without a modified clock, with `workiq_document_coverage` beside the result (#296). A `--since`/`--until` window excludes them, and refused documents never match. Text output now shows document rows and a coverage line for `content list/search/show/sources` and shared search.
