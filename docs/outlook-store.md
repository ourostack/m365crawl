# The new Outlook store: container and calendar layout

This page records what teamscrawl knows about `HxStore.hxd`, the block store that new Outlook for Mac keeps its mail and calendar in. It is the layout the reader (`internal/hxstore`) and the test builder (`internal/hxstore/hxbuild`) are written against. It holds structure and counts only: no subject, name, address, link, id or time-zone name seen on a real machine appears here, and none may be added. The one exception is the fixed 16-byte prefix of an iCal UID, which is a public format constant.

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
| Body preview | word +700 (length +704 †) | 4 + 4 | base T; at most 255 characters | established | exact match in all 106 objects where Teams has a preview; Outlook holds one in the other 354 as well |
| Zone id | +776 (repeated at +1012) | 4 † | numeric | established | 12 values; present in all 3,366 distinct events |
| Zone name | word +780 (length +784); again at +1016 | 4 + 4 | base +1109 for +780; the base of the copy at +1016 is not recorded; Windows zone-name text | established | present in all 3,366 distinct events |
| Show-as | +816 | 4 | 0 free, 1 tentative, 2 busy (only these three observed) | established | 148 of 148 vs Teams |
| Location | word +836 (length +840 †) | 4 + 4 | base T | established | exact match in 397 of the 399 objects where Teams has one, and 20 of 20 against Teams' structured meeting locations; Outlook holds text in 61 objects where Teams has none |
| Subject without cancelled prefix | word +876 (length +880 †) | 4 + 4 | base T | likely | not compared with an independent value |
| Organizer name | word +884 (length +888 †) | 4 + 4 | base T | established | 460 of 460 |
| Organizer address | word +892 (length +896 †) | 4 + 4 | base T | established | 460 of 460 |
| Event type | +904 | 4 † | 0 single, 1 occurrence, 2 exception, 3 master | established | 148 of 148 vs Teams; store-wide 144, 2,785, 360 and 77 |
| Meeting provider label | +980 | not recorded | one constant label on online events | likely | |
| My response | +992 | 4 † | 0 accepted, 1 tentative, 4 not responded; 3 on 6 events undetermined; declined not seen | established | 148 of 148 vs Teams |
| Second copy of the UID | +996 | not recorded | text | likely | |
| Subject | word +1024 (length +1028) | 4 + 4 | base T | established | exact full-text match 460 of 460 |
| Flag word | +1076 | 4 † | bit 4 meaning unknown | not found | differs between versions of one event in 54 pairs |
| All-day | +1082 bit 3 | 1 byte † | bit set | likely | set in 174 of 177 whole-day objects and in 0 of the other 8,412; all-day events sit at midnight UTC; Teams has none to test |
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

## Versions of one event

The store keeps several copies of most objects and leaves old versions in place. 3,263 ids have one distinct content; 103 have more than one. Between versions, last modified (+288) differs in 127 pairs, the +112 stamp in 41 and the flag word at +1076 in 54; start and end never differ.

The working rule, **likely and not established**: the current version is the copy with the highest last-modified (+288), then the highest +112, then the highest file offset (a deterministic tie-break only). Last modified picks the same copy as file order in 78 of 80 groups where it separates copies. "The copy at the highest file offset is current" held in 3,291 of 3,293 calendar groups, but an experiment on mail objects showed new versions written at lower offsets than old ones, so file order is not the primary rule. An edit experiment on a test appointment is needed to establish the rule.

## Unlocated fields and the experiment each needs

| Field | What is known | Experiment |
| --- | --- | --- |
| Deleted events | no tombstone class and no deleted flag found | create an appointment, delete it, copy the store before and after, compare |
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

- Research of 2026-10-05, Outlook 16.115, one store, one machine. Supersedes the first pass where they differ: the +20 word is a series key and the per-event detail link is +180; the zone is also a name string at +780; the distinct event count is 3,366 plus 729 stubs; last modified is preferred to file order.
