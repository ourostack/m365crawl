### Security

- A record archived under a key that held a credential no longer keeps that key after the purge. The row takes the scrubbed key, or, when another row already holds the scrubbed key, it is deleted (the one row the archive deletes). This also covers rows an earlier version cleared but left under the old key, and the calendar losses kept under it (#276).
- The writable archive now always runs with SQLite's `secure_delete` on, so anything a sync replaces, clears or deletes is overwritten with zeros instead of being left in free space. On a copy of a real 2.9 GB archive this did not measurably change sync CPU time. Content that older versions left in free space stays until SQLite reuses that space (#276).
