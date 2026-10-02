# How it works

teamscrawl turns a folder of Chromium database files into a SQLite archive. This page follows one sync from the files on disk to a row you can query. The rules it must keep are in [SPEC.md](../SPEC.md); the code lives under `internal/`, one package per layer.

## The cache

The new Teams app is a Microsoft Edge WebView2 shell. Its web app stores messages, conversations and the activity feed in the browser's IndexedDB, in the app's user data directory (`~/Library/Containers/com.microsoft.teams2/Data/Library/Application Support/Microsoft/MSTeams/EBWebView`). Each profile (`WV2Profile_*`) has one directory per origin, `IndexedDB/https_teams.microsoft.com_0.indexeddb.leveldb`, plus a sibling `.indexeddb.blob` directory for values too large to sit in the database.

Reading a value takes four format layers, from the bottom up:

| Layer | Package | What it reads |
| --- | --- | --- |
| LevelDB | `internal/leveldb` | Sorted key-value tables (`.ldb`), a write-ahead log (`.log`), and a `MANIFEST` that says which files are live. |
| IndexedDB | `internal/indexeddb` | Chromium's key coding on top of LevelDB: database and object-store ids, record keys, blob references. |
| Blink envelope | `internal/indexeddb` | The wrapper Blink puts around each stored value: a version tag, an optional snappy compression layer, an optional pointer to an external blob file, and a trailer on newer versions. |
| V8 | `internal/v8` | V8's structured-clone serialization: the bytes that encode the JavaScript object Teams stored. |

Nothing needs Node, Python or a browser at runtime. Node and Python appear only in tests, as independent readers to compare against.

## Step 1: fingerprint

Before copying anything, teamscrawl lists the files in the origin's two directories and hashes each file's name, size and modification time (ignoring `LOCK` and `LOG*`, which Teams rewrites at every start without changing data). If the hash equals the one recorded by the last successful sync of that source, the cache has not changed and the sync stops for that source with status `unchanged`. This is why an idle sync takes milliseconds.

## Step 2: snapshot

Teams keeps writing while teamscrawl reads, and LevelDB compacts files away. So teamscrawl copies the origin into a private temporary directory (mode 0700) and reads the copy.

- Data files are copied first, then the `MANIFEST`, then `CURRENT` (the file that names the live manifest), then the blob directory. Copying the manifest after the files it describes means the copied manifest rarely names a file the copy lacks; when it does (a compaction removed one mid-copy), the validation below catches it and the copy is retried.
- After the copy it re-reads the source's `CURRENT`, compares the manifest's size, and reads every table and log of the copy to prove it is complete. If anything moved, it tries again, up to three times, then fails with `snapshot_inconsistent`.
- The copy holds Teams' sign-in database, so the directory is removed when the source is done, on failure and on SIGINT or SIGTERM. A sweep at the start of each sync removes leftovers older than one hour from a killed process.

## Step 3: LevelDB without the comparator

Chromium's IndexedDB opens its LevelDB with a custom key comparator (`idb_cmp1`). A reader that assumes bytewise order cannot open such a database as a normal LevelDB. teamscrawl never relies on order across files. Instead it:

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

IndexedDB key coding packs a database id, an object-store id and an index id into a short prefix, followed by the key and, for a record, a value made of a version number and the Blink-serialized value. teamscrawl reads the object-store metadata to find the store ids, then walks the records of the store it wants. A record key it cannot decode is the omission `bad_key`; the rest of the store still reads.

When a value is too large for the database, IndexedDB stores it in a blob file named from the database id and a blob number, all in lowercase hexadecimal: `<db id>/<(number >> 8) as two hex digits>/<number>`. A missing entry or file is the omission `blob_missing`.

## Step 6: the Blink envelope and V8

The stored value starts with a Blink header (`0xff`, a version) and may be wrapped: one wrapper means "the real bytes are in an external blob", another means "snappy-compressed". Blink version 21 adds a short trailer that gives the offset and size of the V8 payload. An envelope teamscrawl does not recognize is the omission `unknown_envelope`, with its first 16 bytes in the detail; it is never guessed.

The V8 payload is decoded by a native Go deserializer for wire versions 13 to 16. It handles everything structured clone produces for plain data: primitives, strings in three encodings, objects, dense and sparse arrays, Date, RegExp, Map, Set, typed-array buffers and back-references. Its output is plain Go values. Host objects and shared objects are named omissions (`v8_host_object`, `v8_shared`), an unknown tag is `v8_unknown_tag`, and an unsupported wire version is `v8_version`. The decoder's output is checked against Node's own `v8.deserialize` in the test suite: on the real cache, over about 18,000 records, with no difference.

## Step 7: mapping

A decoded record is a tree of values. The mappers in `internal/teamsdesktop` turn it into the archive's shapes.

- A reply-chain record holds a map of messages. Each becomes a message row with its sender, times, HTML body, mentions, reactions, files, links, subject, importance, pinned flag and a deep link. Teams stores several fields inconsistently (an array in one record, a JSON string in the next; a number in one, a digit string in another), so the accessors accept both forms and treat anything else as absent. Only a record that lacks its identity (for example no message id) is `unmapped_record`.
- A conversation record becomes a conversation row with its kind, title and members. Channels resolve to `Team › Channel` through the team's own record. The read marker (Teams' consumption horizon) becomes `read_horizon_at`. An untitled chat is named after its members.
- An activity-feed record becomes an activity item. It carries no text; queries join it to its message by conversation and message id.
- Senders and conversation members become people.
- Message HTML becomes plain text: tags are dropped and their text kept (so a mention leaves the person's name), line breaks and block elements become newlines, entities are decoded, whitespace is collapsed, emoji become their text. Bot cards, call events, thread events and call notices become readable lines instead of raw JSON. The full record is kept as `raw_json`.

## Step 8: the archive and full-text search

Each source is applied in one SQLite transaction in batches of 2,000 records. An upsert changes a row only when the incoming version is newer or its content hash differs, so repeating a sync changes nothing. A message that disappears from Teams' cache is left alone, because Teams evicts old messages and the archive is meant to outlive that. A message Teams deletes gets a `deleted_at` timestamp that stays.

Full-text search uses SQLite FTS5 tables (`message_fts`, `conversation_fts`) whose rowids equal the rowids of the `messages` and `conversations` rows they index. The store updates them in the same transaction as the row. That design makes updating an index entry a primary-key lookup instead of a scan, and it has one consequence: those rowids must never change, so the archive must never be vacuumed (`VACUUM` may renumber rowids of tables without an explicit integer key). See SPEC.md section 3.3.

Queries open the archive on a read-only connection, so reads work while a sync writes. A sync holds an exclusive lock file beside the archive, so two syncs cannot run at once.

## Why this holds up

- **Layered and narrow.** Each package does one job and the lower layers know nothing about Teams, so a format change shows up as a specific named omission or error at one layer.
- **Counted, never guessed.** What cannot be decoded is skipped and counted by reason, and the sync report says how many.
- **Tested against independent readers.** The committed fixture was written by a real Edge browser from synthetic data and contains each envelope kind the decoder claims to support. On a real cache, the test suite compares record and message counts with an independent Python reader and every decoded value with Node's V8 deserializer. Results are recorded as counts, never content.
