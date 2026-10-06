package hxbuild

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf16"
)

// The calendar object layouts, written from docs/outlook-store.md and nothing
// else. Every offset counts from the first byte of the object's envelope.
const (
	// ClassEvent and TagEvent name the event object; EventSize is its fixed
	// region, equal to the tag.
	ClassEvent = 0x6b
	TagEvent   = 0x455
	EventSize  = 1109
	// ClassDetail and TagDetail name the detail object an event links to.
	ClassDetail = 0x6c
	TagDetail   = 0x348
	DetailSize  = 840
	// StubSize is the length of an id-less event stub.
	StubSize = 1554

	// Event fixed region.
	evSeriesKey   = 20  // u64, repeated at evSeriesKey2
	evSeriesKey2  = 40  // u64
	evAreaOne     = 104 // u32: size of area one, so T = EventSize + this
	evStamp       = 112 // u64
	evDetailLink  = 180 // u32, repeated at the next two
	evDetailLink2 = 200
	evDetailLink3 = 208
	evLastMod     = 288 // u64 ticks
	evReminder    = 448 // u64 ticks
	evStart       = 584 // u64 ticks
	evEnd         = 592 // u64 ticks
	evPreview     = 700 // string word (base T), length at +4
	evZoneID      = 776 // u32, repeated at evZoneID2
	evZoneName    = 780 // string word (base area one)
	evID          = 820 // string word (base area one)
	evShowAs      = 816 // u32
	evLocation    = 836 // string word (base T)
	evSubjectBare = 876 // string word (base T)
	evOrgName     = 884 // string word (base T)
	evOrgAddr     = 892 // string word (base T)
	evType        = 904 // u32
	evResponse    = 992 // u32
	evSubject     = 1024
	evZoneID2     = 1012 // u32
	evFlags       = 1076 // u32
	evFlagsA      = 1082 // byte: bit 3 all-day, bit 4 cancelled
	evFlagsB      = 1083 // byte: bit 4 online meeting
	stubAreaOne   = 445  // u32@104 of an id-less stub: its string area is empty

	// Detail fixed region.
	dtKey      = 20  // u32
	dtSeries   = 68  // u64
	dtAreaOne  = 104 // u32: the string area base is DetailSize + this
	dtBody     = 600 // string word (base string area), length word has bit 31 set
	dtJoinLink = 700 // string word
	dtDialIn   = 728 // string word

	lengthFlag = 1 << 31
)

// Ticks converts t to .NET ticks (100 ns since 0001-01-01 UTC). The zero time
// gives zero, the value of a field that is not set.
func Ticks(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix()+62_135_596_800)*10_000_000 + uint64(t.Nanosecond()/100) //nolint:gosec // test dates are after 0001, nanoseconds are non-negative
}

// uidPrefix is the fixed 16-byte prefix every iCal UID starts with: a public
// format constant.
var uidPrefix = []byte{0x04, 0x00, 0x00, 0x00, 0x82, 0x00, 0xe0, 0x00, 0x74, 0xc5, 0xb7, 0x10, 0x1a, 0x82, 0xe0, 0x08}

// GlobalObjectID builds an iCal UID in its binary form (NewEvent stores its hex as text): 16 bytes of prefix, four bytes of date (year big-endian, month, day; zero for
// a series or a single event), 8 bytes of creation time and 8 reserved (zero
// here), a four-byte length and the data bytes.
func GlobalObjectID(year, month, day int, data string) []byte {
	tail := append(make([]byte, 16), binary.LittleEndian.AppendUint32(nil, clampU32(len(data)))...)
	return GlobalObjectIDTail(year, month, day, append(tail, data...))
}

// GlobalObjectIDTail builds an id from the prefix, the date and the bytes that
// follow the date, for a test that must reproduce another fixture's ids.
func GlobalObjectIDTail(year, month, day int, tail []byte) []byte {
	b := append([]byte(nil), uidPrefix...)
	b = append(b, byte(year>>8), byte(year), byte(month), byte(day)) //nolint:gosec // truncation to the format's fields is the point; a test passes small values
	return append(b, tail...)
}

// HexID returns the id as the Teams iCalUID writes it: 112 upper-case hex
// characters for a 56-byte id.
func HexID(id []byte) string { return strings.ToUpper(hex.EncodeToString(id)) }

// Attendee is one record of an event's attendee list. A, B and C are the three
// words after the address; B is the response (0 accepted, 1 tentative, 2
// declined, 4 none).
type Attendee struct {
	Name, Address string
	A, B, C       uint32
}

// EventSpec describes one class 0x6b object. Times that are the zero time and
// numbers that are zero are written as zero.
type EventSpec struct {
	// Tag overrides the envelope tag (a test of an unknown layout); zero means
	// TagEvent. The object keeps the fixed size EventSize either way.
	Tag uint16
	// ID is the binary iCal UID (a global object id, 56 bytes for a normal
	// one); it must not be empty (see NewEventStub). The store holds it as
	// text: the upper-case hex of these bytes in UTF-16LE with a terminator,
	// and the length word at +824 counts those bytes.
	ID                           []byte
	SeriesKey                    uint64
	Stamp                        uint64
	DetailKey                    uint32
	LastModified, Start, End     time.Time
	ReminderMinutes              int
	ZoneID                       uint32
	ZoneName                     string
	ShowAs, EventType, Response  uint32
	Flags                        uint32
	AllDay, Cancelled, Online    bool
	Subject, SubjectBare         string
	Location                     string
	OrganizerName, OrganizerAddr string
	Preview                      string
	Attendees                    []Attendee
	// ExtraWord, when not zero, is the offset of a string word (980 or 772 in the real
	// store) whose string is written after the bare subject and before the attendee list,
	// so the list does not follow the string at +876. ExtraString is its text.
	ExtraWord   int
	ExtraString string
	// AreaOneSize is the size of area one (the word at +104). Zero means as big
	// as the id and the zone name need; a bigger value pads with zeros, which
	// moves the string area and so its alignment.
	AreaOneSize int
	// HighBitLengths sets bit 31 of every string length word.
	HighBitLengths bool
}

// NewEvent builds an event object. It panics on a mistake in the spec: an empty
// id, an area one that is too small, a name or address over 255 bytes.
func NewEvent(s EventSpec) *Object {
	if len(s.ID) == 0 {
		panic("hxbuild: an event needs an id")
	}
	tag := s.Tag
	if tag == 0 {
		tag = TagEvent
	}
	o := NewObject(ClassEvent, tag, EventSize)
	id, zone := UTF16Z(HexID(s.ID)), UTF16Z(s.ZoneName)
	if s.AreaOneSize == 0 {
		s.AreaOneSize = len(id) + len(zone)
	}
	if s.AreaOneSize < len(id)+len(zone) {
		panic("hxbuild: area one is too small for the id and the zone name")
	}
	o.PutU32(evAreaOne, clampU32(s.AreaOneSize))
	o.Append(id)
	o.Append(zone)
	o.Append(make([]byte, s.AreaOneSize-len(id)-len(zone)))
	o.PutU32(evID, 0)
	o.PutU32(evID+4, clampU32(len(id)))
	o.PutU32(evZoneName, clampU32(len(id)))
	o.PutU32(evZoneName+4, clampU32(len(zone)))

	o.PutU64(evSeriesKey, s.SeriesKey)
	o.PutU64(evSeriesKey2, s.SeriesKey)
	o.PutU64(evStamp, s.Stamp)
	for _, at := range []int{evDetailLink, evDetailLink2, evDetailLink3} {
		o.PutU32(at, s.DetailKey)
	}
	o.PutU64(evLastMod, Ticks(s.LastModified))
	o.PutU64(evReminder, uint64(s.ReminderMinutes)*600_000_000) //nolint:gosec // a test passes a small non-negative number of minutes
	o.PutU64(evStart, Ticks(s.Start))
	o.PutU64(evEnd, Ticks(s.End))
	o.PutU32(evZoneID, s.ZoneID)
	o.PutU32(evZoneID2, s.ZoneID)
	o.PutU32(evShowAs, s.ShowAs)
	o.PutU32(evType, s.EventType)
	o.PutU32(evResponse, s.Response)
	o.PutU32(evFlags, s.Flags)
	o.PutU8(evFlagsA, flag(s.AllDay, 3)|flag(s.Cancelled, 4))
	o.PutU8(evFlagsB, flag(s.Online, 4))

	base := EventSize + s.AreaOneSize
	put := func(wordOff int, text string) {
		o.PutStringWord(wordOff, base, text)
		n := clampU32(len(UTF16Z(text)))
		if s.HighBitLengths {
			n |= lengthFlag
		}
		o.PutU32(wordOff+4, n)
	}
	put(evSubject, s.Subject)
	put(evLocation, s.Location)
	put(evOrgName, s.OrganizerName)
	put(evOrgAddr, s.OrganizerAddr)
	put(evPreview, s.Preview)
	put(evSubjectBare, s.SubjectBare)
	if s.ExtraWord != 0 {
		put(s.ExtraWord, s.ExtraString)
	}
	o.Append(attendeeList(s.Attendees))
	return o
}

func flag(on bool, bit uint) uint8 {
	if on {
		return 1 << bit
	}
	return 0
}

// attendeeList encodes a u32 count and the records.
func attendeeList(as []Attendee) []byte {
	out := binary.LittleEndian.AppendUint32(nil, clampU32(len(as)))
	for _, a := range as {
		for _, text := range []string{a.Name, a.Address} {
			b := utf16LE(text)
			if len(b) > 255 {
				panic("hxbuild: an attendee name or address is longer than 255 bytes")
			}
			out = append(out, byte(len(b))) //nolint:gosec // checked against 255 above
			out = append(out, b...)
		}
		out = binary.LittleEndian.AppendUint32(out, a.A)
		out = binary.LittleEndian.AppendUint32(out, a.B)
		out = binary.LittleEndian.AppendUint32(out, a.C)
	}
	return out
}

// utf16LE encodes s as UTF-16LE without a terminator.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// NewEventStub builds an id-less class 0x6b object: StubSize bytes, a series key,
// no strings and no detail link.
func NewEventStub(seriesKey uint64) *Object {
	o := NewObject(ClassEvent, TagEvent, StubSize)
	o.PutU32(evAreaOne, stubAreaOne)
	o.PutU64(evSeriesKey, seriesKey)
	o.PutU64(evSeriesKey2, seriesKey)
	return o
}

// DetailSpec describes one class 0x6c object. An empty JoinLink, DialIn or
// BodyHTML is written as a length of zero.
type DetailSpec struct {
	Key       uint32
	SeriesKey uint64
	JoinLink  string
	DialIn    string
	BodyHTML  string
	// Lead is the number of zero bytes between the fixed region and the string
	// area (the word at +104); it moves the strings to odd or even offsets.
	Lead int
}

// NewDetail builds a detail object. The join link and the dial-in number are
// UTF-16LE with a terminator; the body is UTF-8 and its length word has bit 31
// set.
func NewDetail(s DetailSpec) *Object {
	o := NewObject(ClassDetail, TagDetail, DetailSize)
	o.PutU32(dtKey, s.Key)
	o.PutU64(dtSeries, s.SeriesKey)
	o.PutU32(dtAreaOne, clampU32(s.Lead))
	o.Append(make([]byte, s.Lead))
	base := DetailSize + s.Lead
	text := func(wordOff int, data []byte, lenBits uint32) {
		o.PutU32(wordOff, clampU32(o.Len()-base))
		o.PutU32(wordOff+4, clampU32(len(data))|lenBits)
		o.Append(data)
	}
	utf := func(wordOff int, str string) {
		if str == "" {
			text(wordOff, nil, 0)
			return
		}
		text(wordOff, UTF16Z(str), 0)
	}
	utf(dtJoinLink, s.JoinLink)
	utf(dtDialIn, s.DialIn)
	if s.BodyHTML == "" {
		text(dtBody, nil, 0)
	} else {
		text(dtBody, []byte(s.BodyHTML), lengthFlag)
	}
	return o
}
