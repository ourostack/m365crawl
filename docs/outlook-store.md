# The new Outlook store: container and calendar layout

This page records what m365crawl knows about `HxStore.hxd`, the block store that new Outlook for Mac keeps its mail and calendar in. It is the layout the reader (`internal/hxstore`) and the test builder (`internal/hxstore/hxbuild`) are written against. It holds structure and counts only: no subject, name, address, link, id or time-zone name seen on a real machine appears here, and none may be added. The one exception is the fixed 16-byte prefix of an iCal UID, which is a public format constant.

## Where this comes from

The block container (file header, block header, the two checksums, the LZ4 payload) follows the public write-up at github.com/ukd1/hxstore-reverse-engineering (MIT licence, notes taken on Outlook 16.107). Everything about the objects inside the blocks, the framing between them and the calendar fields was reverse-engineered in this project, on a private copy of one store written by Outlook 16.115, by comparing event objects with the Teams calendar records of the same account. The Teams records were the oracle: a field is "established" only where its value matched Teams on every object that could be compared. No code and no data were copied from any reference, and nothing from the store is committed.

Confidence labels used below:

- **established**: matched an independent value (Teams, or a structural rule that held on every object) with no contradiction.
- **likely**: consistent in the counts given, but with no independent value to check against, or with a few unexplained misses.
- **not found**: looked for and not located. The experiment that would locate it is listed in the last section.

Counts are objects unless the text says distinct events. "vs Teams" means compared with the Teams calendar record of the same event.

## Container

All integers are little-endian.

### File header (0x40 bytes)

| Offset | Width | Meaning |
| --- | --- | --- |
| +0x00 | 8 | magic, the ASCII word `Nostromo` |
| +0x08 | 1 | version byte; `i` (0x69) on the measured store |
| +0x38 | 8 | page size; 4096 on the measured store |

The other header bytes are not interpreted. The block scan does not depend on pages: it looks for the block magic. The version byte and the page size are tripwires for a changed layout.

### Block

A block is a 40-byte (0x28) header followed by an LZ4 block payload. Blocks are not assumed to sit on page boundaries.

| Offset | Width | Meaning |
| --- | --- | --- |
| +0x00 | 4 | header checksum: CRC-32 (IEEE) over bytes 0x04 up to but not including 0x20 |
| +0x04 | 4 | payload checksum: CRC-32 (IEEE) over the byte range `[0x08, 0x28 + compressed length)`, that is from the magic through the end of the payload |
| +0x08 | 8 | block magic `05 6a 70 3b 64 45 02 5d` |
| +0x10 | 4 | block type; 8 on every block the reader parses |
| +0x14 | 4 | compressed (payload) length |
| +0x18 | 4 | inflated length |
| +0x1c | 4 | constant 4 |
| +0x20 | 8 | not interpreted |

A block is valid when the header fits, the constant is 4, the type is 8, the inflated length is between 1 and 32 MiB, the payload fits in the file, both checksums match and the payload decodes to exactly the inflated length. On a store copied while Outlook was running, 16,884 of 16,915 blocks found were valid; the other 31 were rejected: 9 for a header checksum mismatch and 22 for a block type other than 8. Both checksum ranges were confirmed on that copy: the reader verified and inflated all 16,884 blocks identically to an independent decoder.

### LZ4

The payload is a raw LZ4 block (not the framed format the `lz4` program writes): sequences of a token byte (high nibble literal count, low nibble match length minus four, 15 meaning extension bytes follow), the literals, a 16-bit match distance and the match length extension. The last sequence carries literals only. The decoder must produce exactly the inflated length, never read a match outside its output, and never allocate beyond the 32 MiB cap. The format is public.

### Payload framing

An inflated payload is not only objects. Measured on the 16,884 valid blocks (counts only):

- **Object**: a `u32` length L, then L bytes starting at the envelope. The envelope repeats L, so the length word is a validating rule. With it, 264,887 objects tile the payloads with no nesting and cover 91.0% of payload bytes.
- **Trailer**: after an object comes a constant 11 bytes, `00 00 00 00 00 01 00 00 00 00 01`. 236,922 gaps between objects are exactly this constant. Whether it is better read as the end of the object before or the start of the object after is not decidable from these facts; the object positions are the same either way.
- **Head**: before the first object a payload has a head. 9,275 heads are exactly 15 bytes, with an unknown meaning. Longer heads of 15 + 4k bytes, up to 64, that end in the trailer constant also occur (lengths 31, 35, 39, 43, 51 and 59 were seen; 3,802 heads end in the constant). The reader accepts a head as framing if it is 15 bytes, or is 15 + 4k bytes, at most 64, and ends in the trailer.
- **Other bytes**: 953 payloads (10.6 MB) hold no object and use a different, undecoded record form. 9,615 gaps between objects are 64 bytes or longer (14.2 MB), also a different record form. Tails after the last object total 8.0 MB. In all, 37,393,086 payload bytes belong to no object.

Object extent and the offset origin: **every offset in this document counts from the first byte of the object's envelope**, the `u16 5` that follows the length word. The length word is at -4.

### Object envelope (12 bytes)

| Offset | Width | Meaning |
| --- | --- | --- |
| +0 | 2 | marker, 5 |
| +2 | 2 | tag |
| +4 | 4 | length L, the same value as the length word before the object |
| +8 | 2 | 0 |
| +10 | 2 | class |

The tag is a pure function of the class (133 tags for 133 classes in the measured store) and never exceeds L. It equals the size of the object's fixed region. A different tag for the same class therefore means a layout change, which is what the version guard keys on. The public notes say eight zero bytes follow the class; that is wrong for event objects. A first count over the whole store found 139 distinct (tag, class) pairs; the later count over validated objects found 133 classes with 133 tags, which is the figure the tag rule rests on.

Pinned pairs: class 0x6b with tag 0x455 (1109 bytes, the event) and class 0x6c with tag 0x348 (840 bytes, the event's detail). Outlook builds differ in tags: the public notes, taken on 16.107, give different tags for folder, message-body and message-metadata classes than 16.115 does, while the classes agree.

### Strings and times

- A string field is a 4-byte offset word, relative to a base that depends on the field, and (by the convention of the second research pass) a 4-byte length word right after it: the byte length including the terminator, often with bit 31 set. The earlier pass found no length word after the string-area offsets; length words were recorded by the research only at +824, +1028 and +784 in the event and +604 and +704 in the detail object; every other length word in the tables below is inferred at the same distance and marked with a dagger (†).
- Text is NUL-terminated UTF-16LE. Strings are not 2-byte aligned: 163 of 546 join links start at an odd offset. A reader scans bytes, and bounds the scan by the object length.
- **A string can be absent.** The length word at the offset word + 4 (bit 31 masked) is zero exactly when the string is absent, and then the offset word is 0 too, with no text stored for it. Offset 0 is the first string of the string area, so reading an absent string by its offset word returns that first string: the subject as first written (the area only grows, so a rename leaves the old subject at offset 0 and appends the new one). A reader must test the length word before it reads. Measured on the copy taken 2026-10-07 after the first rename (6,823 id-carrying event objects, all copies counted): the length word is zero in 230 objects for the body preview (+700), 730 for the location (+836), 14 for the bare subject (+876) and none for the organizer name, organizer address and subject; in every one of those the offset word is 0, and in no object is a zero length word paired with a non-zero offset. Every non-zero length word is the string's byte length including the terminator (the terminator sits at the stated end in 6,593 of 6,593 previews, 6,093 of 6,093 locations, 6,809 of 6,809 bare subjects, and all organizer names, addresses and subjects). A present string can also have offset 0 (the subject in 5,786 objects, the preview in 1,006), so the offset word cannot say "absent"; only the length word can. Known cause of an earlier error in this page: the location and preview rows below were first matched against Teams on objects where both had text, and the "Outlook holds text where Teams has none" counts included absent strings read as the subject.
- Times are 8-byte .NET ticks (100 ns since 0001-01-01), UTC.

## Event object (class 0x6b, tag 0x455)

The store holds 8,589 objects of this class: singles, masters, occurrences and exceptions together. 7,860 carry an id and 729 are id-less stubs (718 of them 1,554 bytes long, belonging to 5 series keys, with no strings and no detail link). There are 3,366 distinct ids.

### Layout of the object

The fixed region is 1109 bytes. After it come two variable areas:

- **Area one** starts at +1109.
- **The string area**, called T below, starts at `T = 1109 + u32@104`. The word at +104 was 813 in 239 objects and took at least seven other values, so it must be read from each object and never assumed.

The id word at +820 is relative to +1109 (area one). The subject, location, organizer, preview and the other text fields named "base T" are relative to T. The attendee list sits in the string area after the bare subject and an optional extra string (see Attendees).

### Field table

"Base" names what the offset word is added to. A dagger (†) on a width or a length word means the research recorded the value but not that width or word; the builder writes it and nothing contradicts it.

| Field | Offset | Width | Coding | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- |
| Id (iCal UID) | word +820, length +824 | 4 + 4 | base +1109; the global object id as upper-case hex in UTF-16LE text, length in bytes with the terminator (see below) | established | equals the Teams `iCalUID` in 460 of 460 matched objects (148 events); on the whole store, 148 of 149 distinct Teams iCal UIDs |
| Series key | +20 (repeated at +40) | 8 | opaque | established | 222 values over 3,366 ids; no value spans two series |
| Change stamp | +112 | 8 | opaque, rises with every write | likely | takes only 4 values in class 0x6b and is 0 in 7,094 objects; established on class 0x4f by a before-and-after experiment |
| Detail link | +180 (repeated at +200 and +208; +184 is 0) | 4 | equals the word at +20 of one detail object | established | 7,860 of 7,860 id-carrying events; all 3,366 distinct ids resolve (3,364 to one key, 2 to two across versions) |
| Second detail reference | +412, +432, +440 | not recorded | not interpreted | not found | present in 1,472 events; meaning undetermined |
| Last modified | +288 | 8 | ticks, UTC | established | within 5 seconds of Teams' last-modified on 449 of 460 matched objects; the tick coding (UTC .NET ticks) is the one established for start and end |
| Reminder lead | +448 | 8 | ticks; 600,000,000 per minute | likely | 12 of 12 vs Teams; present even with no reminder, so not the on/off flag |
| Start | +584 | 8 | ticks, UTC | established | 460 of 460 vs Teams |
| End | +592 | 8 | ticks, UTC | established | 460 of 460 vs Teams |
| Body preview | word +700, length +704 | 4 + 4 | base T; at most 255 characters; absent when the length word is 0 | established | exact match in all 106 objects where Teams has a preview; absent (length 0) in 230 of 6,823 objects on the 2026-10-07 copy, and equal to the subject in none of the 6,593 that hold one. The earlier "Outlook holds one in the other 354" read absent previews as the subject |
| Zone id | +776 (repeated at +1012) | 4 † | numeric | established | 12 values; present in all 3,366 distinct events |
| Zone name | word +780 (length +784); again at +1016 | 4 + 4 | base +1109 for +780; the base of the copy at +1016 is not recorded; Windows zone-name text | established | present in all 3,366 distinct events |
| Show-as | +816 | 4 | 0 free, 1 tentative, 2 busy (only these three observed) | established | 148 of 148 vs Teams |
| Location | word +836, length +840 | 4 + 4 | base T; absent when the length word is 0 | established | exact match in 397 of the 399 objects where Teams has one, and 20 of 20 against Teams' structured meeting locations; absent (length 0) in 730 of 6,823 objects on the 2026-10-07 copy, and equal to the subject in none of the 6,093 that hold one. The earlier "Outlook holds text in 61 objects where Teams has none" read absent locations as the subject |
| Subject without cancelled prefix | word +876 (length +880; 0 when absent, 14 of 6,823 objects) | 4 + 4 | base T | likely | not compared with an independent value |
| Organizer name | word +884 (length +888 †) | 4 + 4 | base T | established | 460 of 460 |
| Organizer address | word +892 (length +896 †) | 4 + 4 | base T | established | 460 of 460 |
| Event type | +904 | 4 † | 0 single, 1 occurrence, 2 exception, 3 master | established | 148 of 148 vs Teams; store-wide 144, 2,785, 360 and 77 |
| Meeting provider label | +980 | not recorded | one constant label on online events | likely | |
| My response | +992 | 4 † | 0 accepted, 1 tentative, 4 not responded; 3 on 6 events undetermined; declined not seen | established | 148 of 148 vs Teams |
| Second copy of the UID | +996 | not recorded | text | likely | |
| Subject | word +1024 (length +1028) | 4 + 4 | base T | established | exact full-text match 460 of 460 |
| Flag word | +1076 | 4 † | bit 4 meaning unknown | not found | differs between versions of one event in 54 pairs |
| All-day | +1082 bit 3 | 1 byte † | bit set | likely | set in 174 of 177 whole-day objects and in 0 of the other 8,412; all-day events sit at midnight UTC and name the zone `UTC` (89 of 89 archived all-day Outlook events, so the zone name says nothing about the owner's zone); the 2026-10-07 all-day probe confirms the bit; Teams has none to test |
| Cancelled | +1082 bit 4 | 1 byte † | bit set | likely | set in 1,964 of 1,964 objects whose subject carries the cancelled prefix, and in 3 others; 850 distinct events |
| Online meeting | +1083 bit 4 | 1 byte † | bit set | established | set in 2,931 distinct events and clear in 435 (of 3,366); agrees with "the detail object has a join link" with 0 disagreements |

Bit numbers count from the least significant bit (mask `1 << n`); the research does not state its numbering, so this reading is unverified.

The id is text. The word at +820 points into area one (base +1109) and the length word at +824 counts bytes, including a 2-byte terminator. The text is the iCal UID's global object id written as upper-case hexadecimal in UTF-16LE: 3,350 ids are 226 bytes (112 characters, a 56-byte global object id) and 16 are 402 bytes (200 characters, 100 bytes). Every one is valid hexadecimal with the standard prefix. Teams writes the same characters in its `iCalUID`, so a reader decodes the UTF-16 text and upper-cases it to get the Teams key; it must not hex-encode the stored bytes. An earlier version of this page called the id binary; it was corrected on 2026-10-05 after a reader built that way matched none of the Teams ids on a real store.

Id structure. All ids start with the same 16 bytes, the public iCal UID prefix `04 00 00 00 82 00 E0 00 74 C5 B7 10 1A 82 E0 08`. Byte positions below are in the decoded global object id (hex characters 32 to 39 for bytes 16 to 19). For occurrences and exceptions, bytes 16 to 19 of the UID carry the occurrence date (3,145 of 3,366 ids); the series id is the id with those 4 bytes zeroed. The research gives the position of the date and not its coding. The test builder writes year (two bytes, big-endian), month and day, which follows the public format and is unverified here.

### Attendees

Inside the event, in the string area after the bare subject (and the extra string, if any), there is a `u32` count, then one record per attendee:

| Part | Width | Meaning |
| --- | --- | --- |
| name length | 1 | byte length of the name |
| name | that many bytes | UTF-16LE, no terminator |
| address length | 1 | byte length of the address |
| address | that many bytes | UTF-16LE, no terminator |
| A | 4 | 1 where Teams says optional (likely) |
| B | 4 | response: 0 accepted, 1 tentative, 2 declined, 4 none (established on 2,001 records) |
| C | 4 | undetermined |

The list starts where the bare subject (+876) ends, except that another string can sit between them. The reader starts at the end of the +876 string and, while the string starting at that position is the target of the +980 or +772 word, skips it and continues from its end; it reads the count there. In a probe of 456 event copies whose count read as over 1,000 when taken right after +876, another string followed it (the string word was +980 in 452 copies and +772 in 4), and after skipping that one string the list parsed cleanly in all 456. +772 and +980 are unidentified string words: their content has not been looked at, and they are not mapped. A rule that starts the list after the furthest end among all the string words was tried and overshot on real data (663 unparsed lists, against 192 before), because a known string word (+700, +836, +884, +892 or +1024) often ends past the list; do not retry it. The list ends at the object end. The count is capped at 8 or 9, so long lists are truncated. 3,206 of 3,366 distinct events (95.2%) parse cleanly with a count above 0. The organizer is not in the list. Outlook holds attendees for 78 events where Teams holds none.

## Detail object (class 0x6c, tag 0x348)

602 objects in the store, fixed region 840 bytes. The string area base is `840 + u32@104`.

### How an event finds its detail object

The word at +180 of the event equals the word at +20 of exactly one detail object (the detail key): 7,860 of 7,860 id-carrying events. 511 detail keys serve one id; 71 are shared by occurrences of one series. The 8 bytes at +68 of the detail object equal the series key at +20 of the event. All 2,931 distinct online events resolve to a detail object with a join link. The first research pass linked the two classes through +20 of the event and +68 of the detail object; that pairs a detail object with a group of events and is superseded by +180.

### Detail fields

| Field | Offset | Width | Coding | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- |
| Detail key | +20 | 4 | opaque | established | see above |
| Series key | +68 | 8 | opaque | established | equals the event's series key |
| String area offset | +104 | 4 | adds to 840 | established | |
| Join link | word +700 (length +704) | 4 + 4 | UTF-16LE text, base 840 + u32@104 | established | 546 of 546 |
| Body | word +600 (length +604) | 4 + 4 | HTML as UTF-8, base 840 + u32@104; bit 31 of the length is set | established | at the stated place in 591 of 600 (9 unexplained); present in 600 of 602 |
| Dial-in toll number | word +728 | 4 | as the join link | likely | 6 of 6 |
| Conference id, toll-free number | not pinned | | | not found | |

The research states the join link as "at +700 (length +704)" and the dial-in at +728; both are read here as offset words on the same base as the body. That reading is unverified. Whether the join link is NUL-terminated in the length is recorded only as "length including the terminator" for string fields in general.

## Account object (class 0x49, tag 0x19d0)

The account object is read for one field, the address of an account signed in to the profile, which links the profile to a Teams account (SPEC.md section 4.2). It is not a calendar object and nothing else of it is mapped.

The fixed region is 6,608 bytes, equal to the tag. The string area starts at `6608 + u32@104` (the word at +104 was 317 in the larger records and 208 in the smaller one). The object's other strings (a version string, the service host names Outlook talks to, a list of file extensions) are packed after it; the reader does not use them.

| Field | Offset | Width | Coding | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- |
| Account address | word +5532, length word +5536 | 4 + 4 | base T = tag + u32@104; UTF-16LE with a terminator, the length word counts bytes including it | established | on the 2026-10-07 copy the class holds 26 objects of this tag, in two records (two keys at +20): 25 copies of one account of 29,666 bytes, whose address equals the Teams profile record's `userPrincipalName`, `mail` and `email` in 25 of 25 (case ignored), and one object of 7,156 bytes that holds the address of another account (a different domain). Both read through the same two words, and the length word equals the text in 26 of 26 |
| Account address, second copy | word +5556, length word +5560 | 4 + 4 | the same text again, written separately | established | equal to the address at +5532 in 26 of 26 objects |

- **One record per account, many versions.** The 25 copies are one record rewritten as the store changes; the other account has one object. Three earlier copies of the store hold 44, 46 and 4 objects of the class, again in two records. The reader keeps the distinct addresses of all the objects that carry a valid one, lower case, and the sync matches them against the Teams profile records.
- **Resynced.** All 26 objects were reached after unknown bytes (the walk's `Resynced` flag), as these large records follow gaps that the framing does not explain. The account reader takes them anyway: the tag is pinned, both copies of the address must agree and each length word must equal its text, which a stray envelope inside another object does not satisfy.
- **A record of the same class and tag can carry no address.** Its string word and length word are both 0, and a string word of 0 reads the first string of the string area (see "A string can be absent"). The reader therefore believes the text only when the length word equals the text's byte length with its terminator, and when the text has exactly one `@` with something on both sides and no space.
- **Account addresses are not the event organizer.** The organizer address of an event (+892) is the address of whoever organized it. The address was located by searching every object of the store for the address of the signed-in Teams user: it also occurs in message and recipient objects, which are many and carry other people's addresses beside it, so the class was chosen by its shape: records rewritten as the store changes, with the service host names beside the address.
- **Not yet known:** what the second account of the measured store is (an account of another kind, or another mailbox of the same sign-in), and what the two copies are each for. Neither changes the link, which needs one Teams account to match.

## Versions of one event

The store keeps several copies of most objects and leaves old versions in place. 3,263 ids have one distinct content; 103 have more than one. Between versions, last modified (+288) differs in 127 pairs, the +112 stamp in 41 and the flag word at +1076 in 54; start and end never differ.

The working rule, **likely and not established**: the current version is the copy with the highest last-modified (+288), then the highest +112, then the highest file offset (a deterministic tie-break only). Last modified picks the same copy as file order in 78 of 80 groups where it separates copies. "The copy at the highest file offset is current" held in 3,291 of 3,293 calendar groups, but an experiment on mail objects showed new versions written at lower offsets than old ones, so file order is not the primary rule. An edit experiment on a test appointment is needed to establish the rule.

## Deleted events

Experiment E11, 2026-10-07, Outlook 16.115, one store, one machine. Two test appointments (no attendees, no body) were created in the live calendar, one timed and one all-day, one of them renamed twice, and both were deleted in Outlook's interface (Delete, confirm). The store file was copied before the creation (s0), after the creation (s1), after each rename (s2, s3), about 4 minutes after the deletion (s4), about 12 minutes after that (s5) and about 30 minutes after s5 (s6). The objects of a probe are found by the global object id text (UTF-16LE hex), the series key at +20 and the detail key; those three reach every class that mentions the event.

**Result: Outlook writes no deletion marker anywhere the reader looks. A deleted event's objects stay in the store, byte for byte, until Outlook compacts the file, and then every object of the event is gone. The only signal is absence.**

| Snapshot | Reader's events | Probe A (timed) | Probe B (all-day) |
| --- | --- | --- | --- |
| s0 before | 3,372 | no object | no object |
| s1 created | 3,374 | 16 event objects, 52 objects in 9 classes | 13 event objects, 38 objects in 9 classes |
| s2 renamed once | 3,374 | 22, 67 | 15, 42 |
| s3 renamed twice, before the deletion | 3,374 | 26, 81 | 20, 50 |
| s4 4 minutes after the deletion | 3,374 | 18, 61 | 10, 27 |
| s5 16 minutes after the deletion | 3,372 | 0, 0 | 0, 0 |
| s6 46 minutes after the deletion | 3,372 | 0, 0 | 0, 0 |

(Counts are objects, old versions included, so they rise with every edit; the s3 to s4 fall is Outlook discarding old copies, not the deletion.) In s5 and again in s6 (which was written after more edits: 16,802 blocks, 43 damaged) no object of any class contains the probe's id text, series key or detail key, and the reader's id set differs from s0 by nothing: 3,372 events, the same ids. Between s0 and s4 the id set gained exactly the two probes and lost nothing; between s4 and s5 it lost exactly the two probes and gained nothing. Between s5 and s6 it did not change.

What was ruled out, by comparing s3 (before the deletion) with s4 (after it) object by object, with the objects hashed by class and bytes:

- **The event object does not change.** The current copy of each probe's class 0x6b object (the one with the highest last-modified, +288) is byte-identical in s3 and s4, so the fixed region (change stamp +112, last modified +288, flag word +1076, flag bytes +1082 and +1083, type, response), the subject and the series key are the same. No deleted flag and no parent or folder change is stored in the object. In s4 the reader returns both probes with the same subject and last-modified time, which is why gone detection cannot read a flag.
- **There is no tombstone or change record of another class that names the event.** Between s3 and s4 the store gained objects in many classes (86 new class 0x4d objects, 45 class 0x55 and others), but none that mentions probe A in any class, and new objects that mention probe B only in classes 0x76 and 0x4008, described next.
- **Some bookkeeping objects change, for one probe only, and are not a usable signal.** Probe B's class 0x4008 objects (tag 0xd6, a per-item header that holds the item's subject, zone and reminder text) were replaced by a shorter form of 833 bytes with the same id and no text, probe B's class 0x76 object (tag 0xd5) grew from 230 to 297 bytes, and its class 0x4005 objects (tag 0xac) disappeared. Probe A's did not change in s4 in any class (its old copies were discarded as for any edit). These classes hold only 7 objects of class 0x4008 and 20 of class 0x76 in the whole store: they track items touched in the last minutes, not events in general, so they do not exist for an event deleted last week.
- **No index drops the event before compaction.** The class 0x6e objects (the calendar-view index, with start and end) of both probes are still present in s4 and none was added; only older copies were discarded.
- **Compaction is what removes it.** The file shrank from 104,857,600 to 88,080,384 bytes between s4 and s5 and the block count from 17,990 to 16,250 (damaged blocks from 80 to 18). The 11 distinct event objects, 6 detail objects and 5 class 0x6e objects that left the store in that step all carry a probe's keys; no object of another event left (older versions of other events were not among them in this step).

What this means for the reader: an event Outlook deleted can be read, unchanged, from a copy taken minutes after the deletion, and is absent from a copy taken after the next compaction. The 12 minutes between s4 and s5 are the most the experiment narrows the compaction to; it was not observed in between, and how often Outlook compacts is not known. A reader marks an event gone only from stores read in which it is missing, so a deletion shows up late, never early.

What can make an event absent without a deletion, and the rule that follows:

- **Eviction.** Outlook keeps a rolling window of events: in s5 the earliest non-master event starts 99.4 days before the copy (99.3 days in s0, 6 hours of clock earlier, so the edge moves with the clock) and the latest 358.5 days after it, while 54 of the 78 series masters start more than 90 days back and are kept as long as their series lives. An event that falls off the back of the window is absent without anyone deleting it. m365crawl therefore judges only events that start within the last 60 days or later (360 of the 3,294 non-master events of s5 start before that and are never judged), and never a master.
- **A damaged or unmapped read.** A torn block (59 to 80 of about 17,500 blocks, about 0.4%, on a store copied while Outlook runs, 18 after compaction) or an event the mapper rejects can hide a live event, and the reader counts a loss only above 2% damaged blocks, so a single read cannot be trusted to show every live event. An event is therefore marked gone only when it is missing from two consecutive reads of different copies that lost nothing; the first miss is only remembered, and a read with a loss, or the event seen again, forgets it. A linked Teams twin is hidden by an Outlook removal (merge rule M0), which is why one miss must never mark.
- **A reset store.** A read that would remember more than 20 events and more than a tenth of the live events it judges remembers none.

Over the six copies, 3,372 events were stable with no other event appearing or disappearing, so the rule marked no ordinary event; one live window of 16 minutes is not a measure of how often a live event is missing from a healthy read, which is why a marked event is not final: it is live again when a later read holds it.

Not established: that compaction always drops a deleted event (only one deletion of each kind was observed, both dropped by the first compaction after them); the behaviour for a deleted occurrence of a series (the probes were single events) and for a series master; whether a meeting cancelled by its organizer (the "cancelled" flag at +1082) is dropped the same way, which the 850 events carrying the cancelled prefix in the measured store suggest it is not, because they are still present.

## Mail

Mail objects sit in the same blocks as the calendar. The reader is `internal/outlookmail`, the builders for its tests are in `internal/hxstore/hxbuild/mail.go`. This section holds structure and counts only, measured on one private copy (Outlook 16.115, 100,663,296 bytes); no subject, name, address, id, body or file name from a real store appears here. Offsets count from the first byte of the object envelope; integers are little-endian. Confidence labels are as above. "Latest copy" means, per object key, the copy with the highest change stamp (+112), then the highest block offset.

### Classes

| Class | Tag (= fixed region) | Role | Distinct keys in the copy |
| --- | --- | --- | --- |
| 0x4f | 0x430 (1072) | message header (what a list reads) | 2,241 (3,988 copies) |
| 0xc9 | 0x60f (1551) | message detail: Message-ID, class, In-Reply-To, sent time | 2,564 |
| 0xca | 0x74e (1870) | body record: inline HTML or the path of a body file | 2,564, same key as the detail |
| 0x16a | 0x318 (792) | attachment record | 4,269 |
| 0x4d | 0x4d0 (1232) | folder | 171 (128 named, 43 unnamed) |
| 0x55 | 0x15e (350) | one recipient | 71,335 (63,702 resolve to a detail object) |

Common words: +20 object key (repeated at +40), +32 parent key, +104 string area lead (the string area base is the fixed region plus this word), +112 u64 change stamp. A class's tag is its fixed size; a mail object with another tag turns mail off for the sync (see the guard in `outlookmail.Collect`).

### Strings

A string is a pair: an offset word at `w` (counted from the string area base: fixed region plus the lead word) and a length word at `w+4`. The length counts bytes, terminator included, and bit 31 is set on most of them (mask it). The string is absent when the length is zero; its offset word is then 0, which points at the first string of the area, so reading it without testing the length returns some other field's text. A present string must have an even length, lie inside the object and end in a UTF-16 NUL (`hxstore.Object.PresentString`). Two exceptions: the invite id at +772 is ASCII hex text with no terminator and a base equal to the tag (no lead word), and a body is UTF-8 or a path (below). Times are .NET ticks; `0x2BCA2875F4373FFF` (the maximum) fills unused slots and is no time.

### Header (class 0x4f)

| Field | Offset | Coding | Confidence |
| --- | --- | --- | --- |
| Detail key (the logical message) | +292 | u32 key of a class 0xc9 object; resolves in 2,241 of 2,241 | established |
| Folder | +532 (copy +552) | u32 key of a class 0x4d object; resolves in 2,241 of 2,241 | established |
| Received | +224 (copy +672) | ticks, UTC; valid in 2,241 of 2,241 | established |
| Subject | +900/+904 | string; absent in 1 of 2,241 | established |
| Sender name | +884/+888 | string | established |
| Sender address (From) | +940/+944 | string; +912 is the list column's address (sender on received mail, first recipient on sent mail) and is ignored | established (+940), likely (+912) |
| Preview | +920/+924 | string; absent in 46 of 2,241 | established |
| Read state | +740 | u32: 1 unread, 0 read; 2 to 7 (22 objects) unknown | established for 0 and 1 |
| Flag | byte +1033 | 0 none, 2 flagged, 1 complete; no real message in the copy has it set | established by a probe only |
| Importance | +752 | 0 low, 1 normal (2,188), 2 high (45) | likely |
| Invite id | +772/+776 | ASCII hex, 112 characters in 244 headers; matches an event id in 208 of 245 | established (join) |
| Inbox mark | +1040 bit 27 | set on 1,038 of 1,038 inbox copies and on none of the others | established |
| Conversation id | none found | no word in the 0x4f, 0xc9 or 0xca fixed region is shared across replies | unknown |

A message has one logical identity, its detail key (2,185 distinct in the copy). It can have several header copies, in To Me, Inbox and Sent Items (2,133 detail keys have one header key, 51 have two, 1 has six). Per key the copy with the highest +112 is current, ties by block offset; file order alone is not reliable.

### Detail (class 0xc9, string base 1551 + lead)

| Field | Offset | Notes |
| --- | --- | --- |
| Message-ID | +1228/+1232 | angle-bracketed; present in 2,241 of 2,241; 2,184 distinct values over 2,185 detail keys |
| Message class | +1236/+1240 | `IPM.Note` 1,956, `IPM.Schedule.*` 283, other 2 |
| In-Reply-To | +1212/+1216 | present in 231 of 2,568 detail objects; equals another message's Message-ID in 147 (likely) |
| Sent time | +728 | ticks; about one second after the stated send time (likely) |
| Received time | +288 | equals the header's in 2,192 of 2,241 |

### Body (class 0xca, string base 1870 + lead)

The record has the same key as the detail object. The body is in one of two forms. Inline: UTF-8 text starting with `<` (after whitespace or a byte order mark), a word pair with bit 31 set in the length. File: a UTF-16LE path `~/Files/S0/2/EFMData/<number>.dat`, in a pair whose position varies by variant (+1524 in 85 messages, +1748 in 12, +1784 in 242). The inline offset also varies (+1668 with a second copy at +1660 in the probe). A reader therefore scans every 4-byte-aligned word pair in [0, 1870) for a target inside the object that is one of the two forms. Counts over 2,241 messages with a loose sniff: inline 1,767, path 339, neither 135 (likely). A body file is gzip (the reader requires the `1f 8b` magic and caps the inflated size at 16 MiB); it lives under the profile's `Files` directory and is read only through `outlookdesktop.OpenReadOnly`. 232 of 339 named files existed in the copy.

### Attachment (class 0x16a, string base 792 + lead)

| Field | Offset | Notes |
| --- | --- | --- |
| Message | +380 | the message's detail key; resolves in 4,219 of 4,269 |
| Name | +608/+612 | string, present in 4,269 of 4,269 |
| Path | +648/+652 | `~/Files/S0/2/Attachments/0/...`; the file exists for 4,027 of 4,269 |
| Size | +568 | u32; equals the file size for 4,027 of 4,027 |
| Download state | +624 | 2 when the file exists (4,027), 5 when not (242) |

There is no content type in the store; the reader derives it from the extension with a fixed table. Names that are a bare GUID with no extension (2,467 of 4,269) are embedded items and count as inline: they do not make a message "have attachments". A separate class 0xf7 repeats the path and adds nothing.

### Folder (class 0x4d, string base 1232 + lead)

Name +1088/+1092, parent +32, well-known type +1160: 0x61 Inbox, 0x63 Archive, 0x64 Drafts, 0x65 Sent Items, 0x67 Deleted Items, 0x7a Junk Email and also To Me (likely: the values agreed with the English names in every named folder; no other language was available). Three account roots hold folder sets in the copy; the measured mail is under one. To Me is a folder of its own that holds separate header copies of the same messages (1,109 latest copies). The reader picks the account's folder set as the root (the first parent that is not itself a folder) whose folders hold the most header copies, and lists only those folders. Of the folders of type 0x7a, the ones that hold a header copy whose message also has a copy in an inbox folder are To Me and the rest are Junk.

Objects of every mail class that were reached after unknown bytes (the walk's `Resynced` flag) are real objects: one that maps cleanly is kept and competes under the version rule like any other copy, and only one that does not map is skipped (a header that does not map leaves the read untrusted for marking messages gone). On one private copy 662 of 1,454 folder objects were reached that way, and skipping them dropped the messages of those folders. A message whose header names a folder object the store holds no copy of is kept all the same, with its folder key, no folder name and the kind `unknown`; more than 5% of the messages in that state is the loss `outlook_mail_folder_missing`.

### Recipients (class 0x55, string base 350 + lead)

One object per recipient. Parent +32 is the message's detail key (63,702 of 71,335 resolve; 2,516 of 2,564 detail keys have at least one). Name +292/+296, address +300/+304 (absent in 16 of 63,702), kind u32 at +316. To versus Cc is not established: kind 2 is the common one (57,098 of 63,702), then 1 (3,913), 3 (1,543) and 7 (1,121), and a probe gave 2 for both a To and a Cc. Only the raw kind is kept.

### What the store does not give, and what the reader does about it

- No stored thread id: threads are rebuilt from In-Reply-To and Message-ID, falling back to the normalized subject plus a shared participant.
- No has-attachments bit that works (a covariant bit is missing in 274 of 325 messages with named attachments): derived from attachment records.
- The cache is partial: the UI showed more messages than the store held in every folder compared, and it back-fills older mail while Outlook runs. Counts taken from it are lower bounds.
- Deleting a message rewrites its header with the Deleted Items folder; the header objects disappear at the next compaction, with no tombstone (one observation).

## Unlocated fields and the experiment each needs

| Field | What is known | Experiment |
| --- | --- | --- |
| Deleted events | **resolved, see "Deleted events"**: no tombstone, no flag, no parent change; the event's objects vanish when Outlook compacts the store | done (experiment E11) |
| Recurrence rule | ticks at +456 and +472 in masters look like a range but did not match the three masters available | a master with a known rule, copied once, compared with the ticks and the objects around them |
| Private flag, reminder on/off | not found | toggle each on a test appointment, copy before and after, diff the fixed region |
| Categories, separate room address, web link | not found | add a category to a test appointment, copy before and after |
| Full-length attendee list | the count is capped at 8 or 9 | an appointment with more than nine invitees; see whether a second list or class holds the rest |
| Meaning of +412, +432, +440, +1076 bit 4, attendee words A and C, the strings at +772 and +980 | not determined | change one setting at a time on a test appointment |
| 729 id-less stubs | belong to 5 series keys; no strings, no detail link | whether another class links them to their series |
| 953 object-less payloads and 9,615 long gaps | a different record form, 24.8 MB together; whether they hold more events is unknown | search them for event ids not found in walked objects |
| Class 0x6e | 6,258 objects, 224-byte fixed part, the same ids (at +192, base 224 + u32@104), the series key, start and end ticks at +152 and +160 | not needed until a consumer wants it |
| Version rule | see above | edit one appointment twice a minute apart and compare the copies |

## Revisions

- Probe of 2026-10-07 (Outlook 16.115): two appointments with no location and no body were created, one of them renamed twice, and the store was copied after each step (synthetic subjects only, no real content). It located the absent-string rule above: for both probes the offset words at +700, +836 and +980 were 0 with length 0, and the subject word at +1024 was 0 with the length of the probe subject. After the first rename the string area held the original subject at offset 0 and the new subject appended at area offset 142 (+1024 then pointed there, the bare subject at +876 followed in a later copy), while the absent strings still read as the original subject. The all-day probe has the all-day bit set, start and end at midnight UTC and the zone name `UTC`.
- Research of 2026-10-05, Outlook 16.115, one store, one machine. Supersedes the first pass where they differ: the +20 word is a series key and the per-event detail link is +180; the zone is also a name string at +780; the distinct event count is 3,366 plus 729 stubs; last modified is preferred to file order.
