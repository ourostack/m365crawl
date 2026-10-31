### Added

- `content fetch doc LINK` reads one OneDrive or SharePoint Office file, such as one linked from a Teams message, through the optional WorkIQ source (#243). Text is kept only when WorkIQ reports `contentProtection: "allowed"` for that exact file, the per-file EXTRACT (Copy) decision Microsoft 365 Copilot makes for encrypted content; encryption that no encrypting label accounts for is refused after a byte check. Only the excerpts WorkIQ released are kept (always partial), with the label, encryption flag, rights value and byte inspection on every check. Every fetch re-checks rights, and a refusal deletes previously kept text. Refusals keep no text and exit 3 with a fixed code. A live view-only refusal has not been observed yet; any value other than `allowed`, or none, refuses.

### Changed

- Writable archives migrate to schema 23 for the WorkIQ document tables. Older read-only archives are not migrated and read as having no documents.
