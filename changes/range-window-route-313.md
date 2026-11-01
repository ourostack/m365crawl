### Changed

- A mail range the archive does not cover (`range_not_covered` routes on `mail list` and mail-including `search`, and the `next` steps and `fix` of `coverage` and `range_coverage` mail entries) now points to a WorkIQ mail window of that range, `workiq sync --source mail --since <start> --until <an hour past what is missing>`, as a read before the cache does (one window per account and range, the same in a read's route and its entries), instead of a whole-account sync that a large mailbox cannot fit on disk (#313). A range without a start keeps the whole-account sync.
