### Added

- `workiq sync --source mail --since T [--until U] [--coverage auto|proof|fast]` fetches only the server mail received in `[T, U)` (whole UTC seconds; `U` defaults to now). Per folder it counts the range and the open range from `T`, then follows the folder's changes from `T` (which can prove the range complete) or lists the range (faster, never proof). A window is proven only after one full round of every folder's changes leaves the range unchanged and the folder tree is the same before and after. The result's new `window` object (`since`, `until`, `clock`, `covered`, `folders[]` with `folder_id`, `mode`, `items_in_window`, `items_open`, `covered`) says what was proven. New codes: `workiq_window_unstable` and `workiq_disk_reserve` (both `resumable: true`), and gaps `workiq_window_unproven`.
- A read whose `--since` is before the cache now routes to a window of exactly the range the cache lacks, with absolute UTC bounds, instead of to whole-account sync; the route disappears once windows prove that range on every folder, and offers `--coverage proof` with its cost after a listing-only window.

### Changed

- Writable archives migrate to schema 24: the `mail_window` attempt kind (a table rebuild on one pinned connection that keeps every attempt ID and reference), window member and coverage tables, staged page body receipts and an `identity_unproven` mark on server mail rows. Older builds refuse a schema-24 archive.
- A window never merges server mail into other mail by Message-ID. Mail the archive already holds is left as it is; other window mail becomes its own server row that no later merge (cache insertion or whole-account sync) joins until a complete whole-account census has seen it. Whole-account censuses no longer carry such rows as their own.
