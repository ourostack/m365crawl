### Fixed

- Outlook for Mac mail reads no longer fail with `outlook_mail_layout_unsupported` on stores that use the `0x431` message header and `0x762` body layouts (seen since 2026-11-12). Every other unknown layout is still refused. The `0x431` header carries no established flag byte, so the message `flag` reads `unknown` for those messages; the next sync re-reads the store (mapper version 6).
