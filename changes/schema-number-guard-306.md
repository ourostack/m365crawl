### Fixed

- An archive whose recorded schema version reached the number of the directed write log, its reply drafts or WorkIQ documents without that feature's tables (possible when parallel changes reorder schema numbers) now reads as not having the feature, instead of failing as incomplete or failing on a missing table (#306). Some but not all of a feature's tables is still reported as incomplete. CI now checks that a pull request's schema numbers are unique, not above `SchemaVersion` and not already used on the base branch.
