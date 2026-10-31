### Added

- Windows new Outlook profiles with several content mailboxes now read each mailbox into its own account (`outlook/<profile>/mailbox-2`, `-3`, …), attributed from the cache's own mailbox identity rather than the database name. The profile's first mailbox keeps `outlook/<profile>` and its public IDs; mailboxes that cannot be attributed still refuse with `outlook_mail_layout_unsupported` and apply nothing.
