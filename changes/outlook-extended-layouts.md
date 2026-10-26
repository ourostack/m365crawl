### Fixed

- Read the macOS Outlook event layout `0x6b/0x459` and mail-body layout
  `0xca/0x752` alongside the older supported layouts, restoring mail and calendar
  reads after the October Outlook update. Unknown layouts remain explicit.
- Every mail read and mail-participating search carries a retained mail-read
  failure as a stale-data notice, even offline, with no matching items or with
  selected fields. A successful mail read clears the notice.
- Unknown Outlook account layouts preserve previously established calendar
  identity and automatic links rather than clearing them as confirmed empty.
- Offline mail search remains compatible with older readable archives without
  retained failure storage.
