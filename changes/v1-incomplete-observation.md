### Fixed

- Incomplete Teams IndexedDB observations retain readable data without treating hidden keys or database names as removals, marking retained calendar events gone, or advancing calendar verification. Truncation stays visible, including an empty partial census; unchanged-cache fingerprints and read memory are invalidated for the corrected decoder.
- A first incomplete calendar read now exposes retained events and normal exact-address Outlook links immediately, while replay remains pending. Archive-only recovery preserves existing sync and cache-fresh clocks and grants no new verified days or coverage window.
