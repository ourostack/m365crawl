### Added

- Opt-in Microsoft Scout session history: `scout sync` copies the session files Scout keeps in your local OneDrive folder into the archive (macOS; Linux with `--root`), and `scout list`, `scout search`, `scout show` and `scout sources` read them offline. Only files already on this computer are read: a cloud-only file is counted in `scout_coverage` and never downloaded. Each version of a session is kept as an immutable observation with its digest, sessions Scout later deletes stay in the archive (`file_state` gone), and token-shaped values are stored as `[redacted]` with the same rules as Teams records. Windows acquisition is refused (`scout_platform_unqualified`) until it is verified. The archive schema is now 21; the new tables are additive.

### Changed

- The help, agent guide, scope page and SPEC now say why private Copilot chat history is not included: Microsoft's export of Copilot interactions supports only an organization-wide application permission that an administrator must approve, not a signed-in user's own access.
