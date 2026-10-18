### Fixed

- The portable OWA acceptance body oracle decodes SQL's hexadecimal BLOB representation before its bounded gzip comparison. Strict null, type, malformed hex/gzip, body and FTS corruption checks remain; production storage and native collector qualification are unchanged.
