### Security

- Redact whole Graph download URL values and temporary authentication URL parameters, including escaped JSON. The new scrub signature causes one full reread of unchanged desktop-cache records after upgrading.

### Changed

- Writable archives migrate to schema21 for optional, disabled-by-default WorkIQ source state. Older read-only archives are not migrated; older binaries are not promised to read the new schema.
