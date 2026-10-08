# How it works

m365crawl turns two local caches into one SQLite archive: the new Teams app's Chromium databases, and the new Outlook for Mac's `HxStore.hxd` store, which holds mail and the calendar. This page follows one sync from the files on disk to a row you can query. The rules it must keep are in [SPEC.md](../SPEC.md); the code lives under `internal/`, one package per layer.

A sync reads Teams first (steps 1 to 7b), then each Outlook profile (steps 7c and 7d), and writes each source in its own transaction (step 8). A failure in one source never undoes another.

## The Teams cache

The new Teams app is a Microsoft Edge WebView2 shell. Its web app stores messages, conversations and the activity feed in the browser's IndexedDB, in the app's user data directory (`~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView` on macOS, `%LOCALAPPDATA%\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView` on Windows). Each profile (`WV2Profile_*`) has one directory per origin, `IndexedDB/https_teams.microsoft.com_0.indexeddb.leveldb`, plus a sibling `.indexeddb.blob` directory for values too large to sit in the database.

Reading a value takes four format layers, from the bottom up:

| Layer | Package | What it reads |
| --- | --- | --- |
| LevelDB | `internal/leveldb` | Sorted key-value tables (`.ldb`), a write-ahead log (`.log`), and a `MANIFEST` that says which files are live. |
| IndexedDB | `internal/indexeddb` | Chromium's key coding on top of LevelDB: database and object-store ids, record keys, blob references. |
| Blink envelope | `internal/indexeddb` | The wrapper Blink puts around each stored value: a version tag, an optional snappy compression layer, an optional pointer to an external blob file, and a trailer on newer versions. |
| V8 | `internal/v8` | V8's structured-clone serialization: the bytes that encode the JavaScript object Teams stored. |

Nothing needs Node, Python or a browser at runtime. Node and Python appear only in tests, as independent readers to compare against.

## Step 1: fingerprint

Before copying anything, m365crawl lists the files in the origin's two directories and hashes each file's name, size and modification time (ignoring `LOCK` and `LOG*`, which Teams rewrites at every start without changing data). If the hash equals the one recorded by the last successful sync of that source, the cache has not changed and the sync stops for that source with status `unchanged`. This is why an idle sync takes milliseconds.

## Skipping records that did not change

A fingerprint says only that some file changed. On a busy account the cache changes all day, yet almost every record in it is the same bytes as at the last sync. So a sync remembers, for each record it read in full, a short digest of the record's payload and what the record did to the archive: the rows it produced (with the hash each row holds) and the people it named, kept in the same transaction as the rows. The next sync computes the digest again and, when it matches and the rows still hold what was left in them, skips decoding, mapping, scrubbing and the writes for that record and counts it as seen and unchanged, exactly as reading it again would. Only the records that changed are read in full. The digest covers a signature of the decoder version, the derivation version and every table of the scrub and deny rules (the signature hashes the rule tables themselves, so adding a key name, a pattern or a deny term changes it and the archived records are scrubbed again under the new rule), so any change to how bytes become rows makes every record be read in full again. Records that share a row (keys that scrub alike, or two records carrying one message) are read in full together, so the row comes out as a full read leaves it. The memory is bounded by the cache and not by the archive's age: a sync forgets the records that left the cache, loads the memory of one database at a time and looks up only the archive rows that memory names. `sync --full-read` forces a full read on demand, even when the cache has not changed since the last sync, and gives the same archive. SPEC.md section 4.1 has the rules.

## Step 2: snapshot

Teams keeps writing while m365crawl reads, and LevelDB compacts files away. So m365crawl copies the origin into a private temporary directory (mode 0700 on macOS, current-user + SYSTEM ACL on Windows) and reads the copy.

- Data files are copied first, then the `MANIFEST`, then `CURRENT` (the file that names the live manifest), then the blob directory. Copying the manifest after the files it describes means the copied manifest rarely names a file the copy lacks; when it does (a compaction removed one mid-copy), the validation below catches it and the copy is retried.
- After the copy it re-reads the source's `CURRENT`, compares the manifest's size, and reads every table and log of the copy to prove it is complete. If anything moved, it tries again, up to three times, then fails with `snapshot_inconsistent`.
- The copy holds Teams' sign-in database, so the directory is removed when the source is done, on failure and on SIGINT or SIGTERM. A sweep at the start of each sync removes leftovers older than one hour from a killed process.

## Step 3: LevelDB without the comparator

Chromium's IndexedDB opens its LevelDB with a custom key comparator (`idb_cmp1`). A reader that assumes bytewise order cannot open such a database as a normal LevelDB. m365crawl never relies on order across files. Instead it:

1. Reads `CURRENT` and the `MANIFEST` it names to learn the live table files and log files.
2. Reads every live table sequentially and every live log (a log is a series of write batches), splitting each internal key into the user key and a trailer holding a sequence number and a record type.
3. Keeps, for each user key, the entry with the highest sequence number. A deletion wins when it is the newest.

The result is the current key-value set whatever the comparator. A torn last record in the active log (Teams was mid-write) is tolerated and counted as the omission `truncated_log_tail`. Table blocks may be uncompressed or snappy; any other compression fails with `unsupported_block_compression` rather than dropping data.

## Step 4: typed and generic databases, the denylist and lazy values

IndexedDB names its databases `Teams:<manager>:react-web-client:<tenantId>:<userId>:<locale>`. The reader first loads only the global metadata to learn each database's name and numeric id. It then reads again. Three databases are typed, with mappers that turn them into tables:

| Manager | Object store |
| --- | --- |
| `replychain-manager` | `replychains-2` |
| `conversation-manager` | `conversations` |
| `activity-manager` | `feed-items` |

Every other database is read generically, unless its name looks like credential material. A database or object store whose name starts with `Teams:auth`, or contains `auth`, `token`, `credential`, `secret`, `cookie`, `msal`, `oneauth`, `key-store`, `keystore`, `keyval` or `session` (ignoring case), is denied: it is never opened for values, decoded or listed, and a sync reports only how many were denied (`denied_database`, `denied_store`). Everything else, calendar, pinned messages, contacts, call history and the rest, goes into the `records` table as canonical JSON, one row per IndexedDB record.

A real origin holds over a hundred other databases and most of the bytes, so the generic read is bounded. A census of the snapshot first estimates how much memory each database needs. Databases are grouped, in order, into batches of at most 64 MiB, and the snapshot is opened once per batch with only that batch's databases kept. A database larger than the budget is a batch of its own. Each opened origin is dropped before the next opens, so memory depends on the largest batch, not on the cache.

Even so, a table value of 512 bytes or more is not kept in memory. The reader remembers where the value lives and re-reads that one block from the snapshot when the record is decoded, through a small cache of recently used blocks. This is why the snapshot must stay in place until the source is finished, and why a full sync peaks near 0.2 GB where loading every database's values took about 0.8 GB.

## Step 5: IndexedDB records

IndexedDB key coding packs a database id, an object-store id and an index id into a short prefix, followed by the key and, for a record, a value made of a version number and the Blink-serialized value. m365crawl reads the object-store metadata to find the store ids, then walks the records of the store it wants. A record key it cannot decode is the omission `bad_key`; the rest of the store still reads.

When a value is too large for the database, IndexedDB stores it in a blob file named from the database id and a blob number, all in lowercase hexadecimal: `<db id>/<(number >> 8) as two hex digits>/<number>`. A missing entry or file is the omission `blob_missing`.

## Step 6: the Blink envelope and V8

The stored value starts with a Blink header (`0xff`, a version) and may be wrapped: one wrapper means "the real bytes are in an external blob", another means "snappy-compressed". Blink version 21 adds a short trailer that gives the offset and size of the V8 payload. An envelope m365crawl does not recognize is the omission `unknown_envelope`, with its first 16 bytes in the detail; it is never guessed.

The V8 payload is decoded by a native Go deserializer for wire versions 13 to 16. It handles everything structured clone produces for plain data: primitives, strings in three encodings, objects, dense and sparse arrays, Date, RegExp, Map, Set, typed-array buffers and back-references. Its output is plain Go values. Host objects and shared objects are named omissions (`v8_host_object`, `v8_shared`), an unknown tag is `v8_unknown_tag`, and an unsupported wire version is `v8_version`. The decoder's output is checked against Node's own `v8.deserialize` in the test suite: on the real cache, over about 18,000 records, with no difference.

## Step 7: mapping

A decoded record is a tree of values. The mappers in `internal/teamsdesktop` turn it into the archive's shapes.

- A reply-chain record holds a map of messages. Each becomes a message row with its sender, times, HTML body, mentions, reactions, files, links, subject, importance, pinned flag and a deep link. Teams stores several fields inconsistently (an array in one record, a JSON string in the next; a number in one, a digit string in another), so the accessors accept both forms and treat anything else as absent. Only a record that lacks its identity (for example no message id) is `unmapped_record`.
- A conversation record becomes a conversation row with its kind, title and members. Channels resolve to `Team › Channel` through the team's own record. The read marker (Teams' consumption horizon) becomes `read_horizon_at`. An untitled chat is named after its members.
- An activity-feed record becomes an activity item. It carries no text; queries join it to its message by conversation and message id.
- Senders and conversation members become people.
- Message HTML becomes plain text: tags are dropped and their text kept (so a mention leaves the person's name), line breaks and block elements become newlines, entities are decoded, whitespace is collapsed, emoji become their text. Bot cards, call events, thread events and call notices become readable lines instead of raw JSON. The full record is kept as `raw_json`.

## Step 7b: the calendar

Three of the generic stores are also the calendar: the calendar events (`Teams:calendar`), the meeting catch-up records (`Teams:meetforwork-manager`) and the meeting recaps (`Teams:meeting-recap-manager`). They stay in `records` like every other generic store, and a derivation step reads them back out of `records` inside the same transaction and writes the `calendar_*` tables. Deriving from `records` instead of from what the sync happened to read makes the result the same whether the sync read every record or skipped the unchanged ones: it maps only the rows whose `updated_at` is this sync's time, so a skipped record is not mapped again.

Teams caches the days the user looked at, not a range, so coverage is a set of days. Each sync reads the days that the live calendar records start on (with SQL, the start time only) and adds them to `calendar_covered_days`, which only ever grows; the window is the span of those days. A record that leaves the cache is treated by the day it was on. If the cache still holds that day, the event was deleted or declined away and gets `removed_at`. If the day is gone too, the cache evicted it and the event stays live. Past events outlive the cache, like old messages.

Teams fetches an event's attendees, body and rooms only for events the user opened, so a later copy is often thinner than an earlier one. The calendar tables keep the richest copy: each part of an event is compared against its own clock, a newer copy replaces it, an older or empty copy never does, and `detail_as_of` says how old the detail is. Recaps are merged field group by field group in the same spirit, and an action item a newer copy no longer lists is marked superseded, never deleted. Recaps link to their event by `iCalUID`, or by time when they have none.

Two profiles can hold the same account, so an event is gone only when none of them still holds it. A sync moves the freshness of the accounts whose cache it read, no others. What the derivation cannot use (a record it cannot map, an event the core refuses) is remembered per record, with a reason, and counted again by every sync until the record changes; a sync that finds the cache unchanged still reports it. The derivation runs in a savepoint, so a calendar failure is a counted loss and the messages of the same sync are kept.

The archive remembers which mapper and which scrub rules the calendar was derived under (`meta.calendar_derivation`). The first sync after an upgrade from an archive without calendar tables fills them from the records it already holds, without the cache. A new mapper version maps every record again, and changed scrub rules blank the derived rows and rebuild them, so nothing an older rule let through survives in a derived copy.

## Step 7c: Outlook and merging two sources

A sync also reads the new Outlook for Mac store (`HxStore.hxd`, one per profile under the Outlook root) after the Teams sources, by default whenever the default Outlook directory exists and holds a profile. `--outlook-root none` (or `M365CRAWL_OUTLOOK_ROOT=none`) turns it off, `--outlook-root DIR` reads DIR, and an explicit `--teams-root` with no `--outlook-root` keeps it off. A default Outlook is best effort: with no Outlook, no Full Disk Access or a store it cannot read, the Teams sync is untouched and Outlook shows as `unavailable` in the report, in `calendar sources` and in `doctor`. m365crawl copies the one store file into a private temporary directory, reads the copy (at most once every five minutes per profile), checks the store version and layout before it maps anything, and deletes the copy on every exit. A store it does not recognize fails that source alone, with a coded error, and applies nothing. Each profile becomes its own calendar account, `outlook/<profile>`, and its events go through the same capture rule as Teams events. Outlook does not mark a deleted meeting: it drops the meeting's objects when it compacts the store, minutes later. Two consecutive reads that lost nothing, of different copies, therefore mark an event gone when both miss it from the store (one miss is only remembered, because a torn copy can hide a live event for a single read), unless it is a series master or starts more than 60 days back (Outlook's own window drops those without a deletion); the event keeps its data, and it is live again if a later read holds it.

An Outlook account joins a Teams account in one of two ways, and never by overlap, because two people invited to one meeting hold the same events. The operator links it (`--outlook-account <tenantId>/<userId>`, with `--outlook-profile` when the root holds several profiles), kept in the archive until `none` ends it. Or the sync links it by proven identity, with the method `address`: the Outlook read keeps the addresses of the accounts signed in to the profile (its account objects), the Teams archive holds the signed-in user's `userPrincipalName`, `mail` and `email` in the record the account keeps of itself, and when one of the profile's addresses equals exactly one Teams account's own address, ignoring case, and no other profile claims that account, the two are linked. No match, two Teams accounts with one address or two profiles with one address leave the profile unlinked, with the notice. An explicit link, and an explicit `none`, always win over the automatic one and are never replaced by it. Both sources key an event by its iCalUID (Teams writes it in lower case and Outlook in upper case, so the Outlook mapper lower-cases it), which is how twins meet. A read merges the twins of a linked account into one item. The schedule comes from the copy modified last (when the two times differ by more than five seconds; otherwise the fresher cache, then Outlook), the detail from the copy with the later detail time, and a field the base copy does not state is filled from the other and listed in `filled_fields` with its source and time. The merge rule is: Teams owns the Teams meeting fields (join link, dial-in, meeting chat id), recordings and recaps never detach on link, attendees and rooms are the union of both copies, and the schedule and other scalars come from the newer copy. A per-event `overridden_fields` list names each field whose value differed and which source won (the key name is not final). Unknown never overrides known, and an older copy's `cancelled: false` is never filled in, because it says nothing about now. A field neither copy stated stays "not known" (`unknown_fields`): the archive stores an unstated flag as unknown, never as false. A meeting Teams saw go stays hidden unless the Outlook copy was edited after the removal (`removed_by` names the sources that saw it go). Coverage is judged per person: a day either source covers is covered.

## Step 7d: Outlook mail

Mail comes from the same private copy of `HxStore.hxd` that the calendar read uses, so each profile's store is copied once per sync. Mail is read after the calendar mapping is released, so the two never hold memory together. If the store has not changed since mail was last read, the mail read is skipped. On Windows mail is not read yet: the sync report carries a note with the code `mail_unsupported_platform`, and Teams and the calendar sync as usual.

The reader (`internal/outlookmail`) maps six object classes: the message header (what a list shows), the message detail (Message-ID, In-Reply-To, sent time), the body record, attachments, folders and recipients. [outlook-store.md](outlook-store.md) has the layout of each.

- **One message, several copies.** Outlook can keep several header copies of one message, for example in Inbox and in To Me. The logical message is the detail object they all point at, and the copy with the highest change stamp wins. A message whose only copy is in To Me is listed there; otherwise its real folder wins and `to_me` is true.
- **Folders.** The store can hold several folder sets. The reader takes the set that holds the most messages and tells To Me from Junk by whether a folder shares messages with the Inbox. A message whose folder object the store does not hold is kept with folder kind `unknown` (`mail list --folder unknown` lists them) and counted; if more than 5% of messages lack a folder, the read is untrusted.
- **Bodies.** A body is inline HTML in the store, or a gzip file under the profile's `Files` directory. Body files are opened read-only, only below `Files`, and inflated up to 16 MiB; a missing file is `body_state: missing` and a damaged one `unreadable`. HTML becomes plain text with scripts and styles dropped, and nothing is ever fetched. The archive keeps the compressed HTML as well as the text, so the text can be derived again when the converter improves.
- **Attachments** are recorded as metadata: name, size, a content type taken from the file extension, and whether Outlook's cache holds the file. The bytes are not copied. Items named by a bare GUID are embedded parts and are marked `inline`; they do not make a message "have attachments".
- **Trust.** Before it maps anything, the reader checks the object layout. A layout it does not know, or a read that covers too little of the store, makes the read untrusted: it still adds and updates messages but never marks one as gone.

The archive keeps a message's content (subject, sender, recipients, times, Message-ID) as first read, and updates its state (folder, read, flag, importance, body when it arrives later) in place. Outlook writes no deletion marker, and its cache also ages old mail out, so absence needs care. For each account and folder the archive records the oldest message the cache still holds (`mail_coverage`). A message missing from two trusted reads of different store copies is marked `gone` if it was received inside that covered range, and `evicted` if it is older. A read that would mark more than 20 messages and more than a tenth of them gone marks none. A message that comes back is live again. Gone and evicted messages stay in the archive.

## Step 8: the archive and full-text search

Each Teams source is applied in one SQLite transaction in batches of 2,000 records, and each profile's mail in one transaction of its own. An upsert changes a row only when the incoming version is newer or its content hash differs, so repeating a sync changes nothing. A message that disappears from Teams' cache is left alone, because Teams evicts old messages and the archive is meant to outlive that. A message Teams deletes gets a `deleted_at` timestamp that stays. By default the archive lives at `~/.m365crawl/m365crawl.db` on macOS and `%LOCALAPPDATA%\m365crawl\m365crawl.db` on Windows; on Windows the archive directory boundary is made private before SQLite writes any real bytes.

Full-text search uses SQLite FTS5 tables (`message_fts`, `conversation_fts`, and `mail_fts` over subject, sender and body text) whose rowids equal the rowids of the `messages`, `conversations` and `mail_messages` rows they index. The store updates them in the same transaction as the row. That design makes updating an index entry a primary-key lookup instead of a scan, and it has one consequence: those rowids must never change, so the archive must never be vacuumed (`VACUUM` may renumber rowids of tables without an explicit integer key). See SPEC.md section 3.3.

Queries open the archive on a read-only connection, so reads work while a sync writes. A sync holds an exclusive lock file beside the archive, so two syncs cannot run at once.

## Why this holds up

- **Layered and narrow.** Each package does one job and the lower layers know nothing about Teams or Outlook, so a format change shows up as a specific named omission or error at one layer.
- **Counted, never guessed.** What cannot be decoded is skipped and counted by reason, and the sync report says how many.
- **Tested against independent readers.** The committed Teams fixture was written by a real Edge browser from synthetic data and contains each envelope kind the decoder claims to support. The Outlook fixtures are built from the documented layout by `scripts/hxfixture`. On a real cache, the test suite compares record and message counts with an independent Python reader and every decoded value with Node's V8 deserializer. Results are recorded as counts, never content.
