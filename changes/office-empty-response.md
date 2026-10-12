### Fixed

- `docs sync` no longer reports a failure for an Office recent, shared or recommended file that is a successful service response listing no documents. The source is now complete with zero rows and reason `office_response_listed_no_documents`, and `source_coverage.notices` explains that the saved response listed none, not that the account has none. Earlier observations from the source stay in the archive as retained. Malformed, error and unknown responses still fail as `office_json_unrecognized`.
