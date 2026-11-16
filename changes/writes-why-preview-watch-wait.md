### Added

- Every write takes `--why` (the same as `--reason`), and `--dry-run` previews a write: the outcome it would have, with who would see it and the text, and nothing is sent or changed.
- `m365crawl watch <chat> --timeout DURATION` waits for a new message in one chat and answers a `wait/1` document, with a `--cursor` to wait again from where it stopped.
