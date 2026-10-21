### Fixed

- Retain a Windows Outlook mail snapshot when an aborted open or failed reader close cannot prove that its lazy table reader was released. Preserve the original failure or cancellation and report the retained snapshot without exposing cached content; healthy and proved-canceled reads still remove their snapshot. Calendar support and mailbox attribution are unchanged.
