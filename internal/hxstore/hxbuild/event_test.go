package hxbuild

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func u64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }

// utf16z reads NUL-terminated UTF-16LE at off.
func utf16z(t *testing.T, b []byte, off int) string {
	t.Helper()
	var units []uint16
	for i := off; ; i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if u == 0 {
			return string(utf16.Decode(units))
		}
		units = append(units, u)
	}
}

func TestTicks(t *testing.T) {
	if Ticks(time.Time{}) != 0 {
		t.Fatal("zero time")
	}
	// 2000-01-01 UTC is 630,822,816,000,000,000 ticks.
	if got := Ticks(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); got != 630_822_816_000_000_000 {
		t.Fatal(got)
	}
	if got := Ticks(time.Date(2000, 1, 1, 0, 0, 1, 500, time.UTC)); got != 630_822_816_010_000_005 {
		t.Fatal(got)
	}
}

func TestGlobalObjectID(t *testing.T) {
	raw := GlobalObjectID(2031, 3, 4, "FIXTURE-EVT-0001")
	id := HexID(raw)
	if len(raw) != 56 || len(id) != 112 || id != strings.ToUpper(id) {
		t.Fatal(len(id), id)
	}
	if !strings.HasPrefix(id, "040000008200E00074C5B7101A82E008") {
		t.Fatal(id)
	}
	// year 2031 = 0x07EF, month 3, day 4; then 16 zero bytes, size 16 little endian.
	if id[32:40] != "07EF0304" || id[40:72] != strings.Repeat("0", 32) || id[72:80] != "10000000" {
		t.Fatal(id)
	}
	if !strings.HasSuffix(id, "464958545552452D4556542D30303031") { // FIXTURE-EVT-0001
		t.Fatal(id)
	}
	if series := HexID(GlobalObjectID(0, 0, 0, "FIXTURE-EVT-0001")); series[32:40] != "00000000" {
		t.Fatal(series)
	}
}

func TestGlobalObjectIDTail(t *testing.T) {
	id := GlobalObjectIDTail(2023, 11, 22, []byte("AB"))
	if HexID(id) != "040000008200E00074C5B7101A82E00807E70B164142" {
		t.Fatal(HexID(id))
	}
}

func fullSpec() EventSpec {
	return EventSpec{
		ID: []byte("ABCD"), SeriesKey: 0x1122334455667788, Stamp: 77, DetailKey: 9,
		LastModified:    time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC),
		Start:           time.Date(2031, 1, 3, 0, 0, 0, 0, time.UTC),
		End:             time.Date(2031, 1, 3, 1, 0, 0, 0, time.UTC),
		ReminderMinutes: 15, ZoneID: 4, ZoneName: "Zed",
		ShowAs: 2, EventType: 1, Response: 4, Flags: 0x10,
		AllDay: true, Cancelled: true, Online: true,
		Subject: "Sub", SubjectBare: "Bare", Location: "Loc",
		OrganizerName: "Org", OrganizerAddr: "o@example.invalid", Preview: "Prev",
		Attendees: []Attendee{{"An", "a@example.invalid", 1, 0, 7}, {"Bo", "b@example.invalid", 0, 4, 0}},
	}
}

func TestNewEventLayout(t *testing.T) {
	spec := fullSpec()
	spec.AreaOneSize = 22 // even, so the string area starts at an odd offset (1109 is odd)
	e := NewEvent(spec).Encode()[4:]
	if len(e) != int(u32(e, 4)) || u16(e, 2) != 0x455 || u16(e, 10) != 0x6b {
		t.Fatalf("envelope %x", e[:12])
	}
	if u32(e, 104) != 22 {
		t.Fatal(u32(e, 104))
	}
	T := 1109 + 22
	// id and zone name live in area one, with offsets relative to 1109.
	id := 1109 + int(u32(e, 820))
	if string(e[id:id+4]) != "ABCD" || u32(e, 824) != 4 {
		t.Fatal("id")
	}
	if utf16z(t, e, 1109+int(u32(e, 780))) != "Zed" || u32(e, 784) != 8 {
		t.Fatal("zone name")
	}
	for off, want := range map[int]string{1024: "Sub", 836: "Loc", 884: "Org", 892: "o@example.invalid", 700: "Prev", 876: "Bare"} {
		if got := utf16z(t, e, T+int(u32(e, off))); got != want {
			t.Fatalf("%d: %q", off, got)
		}
		if int(u32(e, off+4)) != 2*len(utf16.Encode([]rune(want)))+2 {
			t.Fatalf("length at %d: %d", off+4, u32(e, off+4))
		}
	}
	if (T+int(u32(e, 836)))%2 != 1 {
		t.Fatal("expected strings at odd offsets")
	}
	for off, want := range map[int]uint64{20: 0x1122334455667788, 40: 0x1122334455667788, 112: 77, 288: Ticks(spec.LastModified),
		448: 15 * 600_000_000, 584: Ticks(spec.Start), 592: Ticks(spec.End)} {
		if u64(e, off) != want {
			t.Fatalf("u64 at %d", off)
		}
	}
	for off, want := range map[int]uint32{180: 9, 200: 9, 208: 9, 184: 0, 776: 4, 1012: 4, 816: 2, 904: 1, 992: 4, 1076: 0x10} {
		if u32(e, off) != want {
			t.Fatalf("u32 at %d: %d", off, u32(e, off))
		}
	}
	if e[1082] != 0x08|0x10 || e[1083] != 0x10 {
		t.Fatalf("flags %x %x", e[1082], e[1083])
	}
	// The attendee list follows the last string and ends the object.
	list := T + int(u32(e, 876)) + int(u32(e, 880))
	if u32(e, list) != 2 {
		t.Fatal("attendee count")
	}
	p := list + 4
	for _, want := range []struct {
		name, addr string
		a, b, c    uint32
	}{{"An", "a@example.invalid", 1, 0, 7}, {"Bo", "b@example.invalid", 0, 4, 0}} {
		for _, w := range []string{want.name, want.addr} {
			n := int(e[p])
			if string(utf16.Decode(u16s(e[p+1:p+1+n]))) != w {
				t.Fatal("attendee text")
			}
			p += 1 + n
		}
		if u32(e, p) != want.a || u32(e, p+4) != want.b || u32(e, p+8) != want.c {
			t.Fatal("attendee words")
		}
		p += 12
	}
	if p != len(e) {
		t.Fatalf("list ends at %d of %d", p, len(e))
	}
}

func u16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off:]) }

func u16s(b []byte) []uint16 {
	out := make([]uint16, len(b)/2)
	for i := range out {
		out[i] = u16(b, 2*i)
	}
	return out
}

func TestNewEventDefaults(t *testing.T) {
	spec := EventSpec{ID: []byte("A"), Tag: 0x456, HighBitLengths: true, ZoneName: "Z"}
	e := NewEvent(spec).Encode()[4:]
	if u16(e, 2) != 0x456 || len(e) < 1109 {
		t.Fatal("tag override keeps the fixed size")
	}
	if u32(e, 104) != 1+4 || u32(e, 1028) != 2|1<<31 {
		t.Fatal(u32(e, 104), u32(e, 1028))
	}
	if e[1082] != 0 || e[1083] != 0 || u64(e, 288) != 0 {
		t.Fatal("unset fields must be zero")
	}
	// No attendees: a zero count at the end.
	if u32(e, len(e)-4) != 0 {
		t.Fatal("count")
	}
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	f()
}

func TestNewEventPanics(t *testing.T) {
	mustPanic(t, func() { NewEvent(EventSpec{}) })
	mustPanic(t, func() { NewEvent(EventSpec{ID: []byte("ABCD"), AreaOneSize: 4}) })
	mustPanic(t, func() {
		NewEvent(EventSpec{ID: []byte("A"), Attendees: []Attendee{{Name: strings.Repeat("x", 128)}}})
	})
}

func TestNewEventStub(t *testing.T) {
	s := NewEventStub(42).Encode()[4:]
	if len(s) != 1554 || u16(s, 2) != 0x455 || u16(s, 10) != 0x6b || u32(s, 104) != 445 || u64(s, 20) != 42 || u64(s, 40) != 42 ||
		u32(s, 180) != 0 || u32(s, 820) != 0 || u32(s, 824) != 0 {
		t.Fatal("stub")
	}
}

func TestNewDetail(t *testing.T) {
	d := NewDetail(DetailSpec{Key: 5, SeriesKey: 6, JoinLink: "https://example.invalid/j", DialIn: "555", BodyHTML: "<p>x</p>", Lead: 3}).Encode()[4:]
	if len(d) < 840 || u16(d, 2) != 0x348 || u16(d, 10) != 0x6c || u32(d, 20) != 5 || u64(d, 68) != 6 || u32(d, 104) != 3 {
		t.Fatal("fixed part")
	}
	base := 840 + 3
	if utf16z(t, d, base+int(u32(d, 700))) != "https://example.invalid/j" || u32(d, 704) != 2*25+2 {
		t.Fatal("join link")
	}
	if (base+int(u32(d, 700)))%2 != 1 {
		t.Fatal("expected an odd start")
	}
	if utf16z(t, d, base+int(u32(d, 728))) != "555" || u32(d, 732) != 8 {
		t.Fatal("dial-in")
	}
	at := base + int(u32(d, 600))
	if u32(d, 604) != 8|1<<31 || !bytes.Equal(d[at:at+8], []byte("<p>x</p>")) || at+8 != len(d) {
		t.Fatal("body")
	}
	empty := NewDetail(DetailSpec{Key: 1}).Encode()[4:]
	if u32(empty, 704) != 0 || u32(empty, 732) != 0 || u32(empty, 604) != 0 || len(empty) != 840 {
		t.Fatal("empty detail")
	}
}
