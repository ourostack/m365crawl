package outlookcal

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

func mapOK(t *testing.T, o hxstore.Object, d *hxstore.Object) (calendar.Event, MapNotes) {
	t.Helper()
	e, n, err := MapEvent("outlook/Test", o, d)
	if err != nil {
		t.Fatal(err)
	}
	return e, n
}

func TestMapEventFieldByField(t *testing.T) {
	spec := baseSpec(1)
	spec.EventType, spec.ShowAs, spec.Response = 3, 1, 1
	spec.Online, spec.Cancelled = true, true
	e, notes := mapOK(t, ev(spec), det(baseDetail(1)))
	id := hxbuild.HexID(spec.ID)
	lm := at(1, 8)
	for name, ok := range map[string]bool{
		"source":    e.Source == calendar.SourceOutlook && e.AccountID == "outlook/Test",
		"ids":       e.SourceID == id && e.GlobalID == id && e.ICalUID == id && e.SeriesKey == id,
		"type":      e.EventType == calendar.EventMaster,
		"times":     e.Start.Equal(at(5, 9)) && e.End.Equal(at(5, 10)) && e.LastModified != nil && e.LastModified.Equal(lm),
		"strings":   e.Subject == "Fixture subject" && e.Location == "Fixture Room" && e.Organizer == "Fixture Organizer" && e.OrganizerAddress == "organizer@example.invalid" && e.BodyPreview == "Fixture preview",
		"show/resp": e.ShowAs == "tentative" && e.Response == "tentative",
		"flags":     e.AllDay.Is(false) && e.Cancelled.Is(true) && e.IsOnlineMeeting.Is(true),
		"zone":      e.TimeZone == "Pacific Standard Time" && e.TimeZoneIANA == "America/Los_Angeles",
		"detail":    e.OnlineMeetingURL == "https://example.invalid/fixture/join/1" && e.DialInTollNumber == "Fixture dial-in" && e.BodyHTML == "<p>Fixture body</p>" && e.BodyType == "html" && strings.Contains(e.BodyText, "Fixture body"),
		"not set":   e.IsOrganizer == calendar.TriUnknown && e.IsPrivate == calendar.TriUnknown && e.HasAttachments == calendar.TriUnknown && e.OriginalStart == nil && e.UTCOffset == "",
		"declared":  e.UnknownDeclared,
		"notes":     notes == MapNotes{CancelledSubjectsMatch: true, SeriesID: id, SeriesWord: spec.SeriesKey},
		"key":       calendar.Key(e) == id+"|",
	} {
		if !ok {
			t.Errorf("%s wrong: %+v", name, e)
		}
	}
	var atts []map[string]string
	if err := json.Unmarshal([]byte(e.AttendeesJSON), &atts); err != nil || len(atts) != 2 ||
		atts[0]["name"] != "Fixture One" || atts[0]["type"] != "optional" || atts[0]["response"] != "accepted" ||
		atts[1]["address"] != "two@example.invalid" || atts[1]["type"] != "" || atts[1]["response"] != "none" {
		t.Fatalf("%s %v", e.AttendeesJSON, err)
	}
	// The attendee list is said to be what the store holds, with no invented total.
	var note map[string]any
	if err := json.Unmarshal([]byte(e.DetailRawJSON), &note); err != nil || note["attendees_stored"] != float64(2) || note["attendees_maybe_truncated"] != false {
		t.Fatalf("%s %v", e.DetailRawJSON, err)
	}
	calendar.AssertEveryFieldClassified(t, e, calendar.FieldMeetingChatID) // the link is not a Teams link: no thread id
}

func TestUnlocatedFieldsStayUnknown(t *testing.T) {
	spec := baseSpec(2)
	spec.AllDay = true
	spec.Start, spec.End = at(6, 0), at(7, 0)
	for _, e := range []calendar.Event{
		first(mapOK(t, ev(spec), det(baseDetail(2)))),
		first(mapOK(t, ev(baseSpec(2)), nil)),
	} {
		set := map[calendar.Field]bool{}
		for _, f := range e.Unknown {
			set[f] = true
		}
		for _, f := range UnlocatedFields {
			if !set[f] {
				t.Errorf("%s must be unknown", f)
			}
		}
		if e.ReminderMinutes != nil || e.UTCOffset != "" || e.ShortJoinURL != "" || e.LocationsJSON != "" || e.AttachmentsJSON != "" || e.CategoriesJSON != "" || e.RecurrenceJSON != "" {
			t.Errorf("an unlocated field has a value: %+v", e)
		}
		for _, f := range UnlocatedFlags {
			found := false
			for _, name := range calendar.UnknownFields(e) {
				found = found || name == string(f)
			}
			if !found {
				t.Errorf("flag %s must be unknown", f)
			}
		}
	}
}

func first[T any](v T, _ MapNotes) T { return v }

func TestMapEventCodes(t *testing.T) {
	for code, want := range map[uint32]string{0: calendar.EventSingle, 1: calendar.EventOccurrence, 2: calendar.EventException, 3: calendar.EventMaster} {
		s := baseSpec(1)
		s.EventType = code
		if e, n := mapOK(t, ev(s), nil); e.EventType != want || n.EventTypeUnknown {
			t.Errorf("type %d: %q", code, e.EventType)
		}
	}
	for code, want := range map[uint32]string{0: "free", 1: "tentative", 2: "busy"} {
		s := baseSpec(1)
		s.ShowAs = code
		if e, _ := mapOK(t, ev(s), nil); e.ShowAs != want {
			t.Errorf("show-as %d: %q", code, e.ShowAs)
		}
	}
	for code, want := range map[uint32]string{0: "accepted", 1: "tentative", 4: "none"} {
		s := baseSpec(1)
		s.Response = code
		if e, _ := mapOK(t, ev(s), nil); e.Response != want {
			t.Errorf("response %d: %q", code, e.Response)
		}
	}
	// Values outside the layout stay unknown and are counted.
	s := baseSpec(1)
	s.EventType, s.ShowAs, s.Response = 9, 9, 3
	e, n := mapOK(t, ev(s), nil)
	if e.EventType != "" || e.ShowAs != "" || e.Response != "" || !n.EventTypeUnknown || !n.ShowAsUnmapped || !n.ResponseUnmapped {
		t.Fatalf("%+v %+v", e, n)
	}
	calendar.AssertEveryFieldClassified(t, e)
	if u := calendar.UnknownFields(e); !contains(u, "show_as") || !contains(u, "response") {
		t.Fatal(u)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestMapEventZone(t *testing.T) {
	s := baseSpec(1)
	s.ZoneName = "Fixture Unknown Time"
	e, n := mapOK(t, ev(s), nil)
	if e.TimeZone != "Fixture Unknown Time" || e.TimeZoneIANA != "" || n.UnknownZone != "Fixture Unknown Time" || !contains(calendar.UnknownFields(e), "time_zone_iana") {
		t.Fatalf("%+v %+v", e, n)
	}
	s.ZoneName = ""
	e, _ = mapOK(t, ev(s), nil)
	if e.TimeZone != "" || !contains(calendar.UnknownFields(e), "time_zone") {
		t.Fatal("an empty zone name is unknown")
	}
	// The zone word points outside the object: the zone is unknown, the event still maps.
	o := hxbuild.NewEvent(baseSpec(1))
	o.PutU32(780, 1<<30)
	if e, _ := mapOK(t, obj(o), nil); e.TimeZone != "" {
		t.Fatal("zone out of range")
	}
	for name, want := range map[string]string{"UTC": "UTC", "Pacific Standard Time": "America/Los_Angeles", "India Standard Time": "Asia/Kolkata", "Europe/London": "Europe/London", "PacificSt": "America/Los_Angeles"} {
		if got, loc, ok := ZoneIANA(name); !ok || got != want || loc == nil {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
	if _, _, ok := ZoneIANA("Nowhere Standard Time"); ok {
		t.Fatal("unknown zone resolved")
	}
	// Every table row resolves to a zone Go can load.
	for name := range windowsZones {
		if _, loc, ok := ZoneIANA(name); !ok || loc == nil {
			t.Errorf("table row %q does not resolve", name)
		}
	}
}

func TestMapEventAllDay(t *testing.T) {
	s := baseSpec(1)
	s.AllDay = true
	s.Start, s.End = at(6, 0), at(8, 0)
	e, n := mapOK(t, ev(s), nil)
	if !e.AllDay.Is(true) || e.StartDate != "2031-03-06" || e.EndDate != "2031-03-08" || n.AllDayUnaligned {
		t.Fatalf("%+v", e)
	}
	s.Start, s.End = at(6, 0), at(6, 0)
	if e, _ := mapOK(t, ev(s), nil); e.EndDate != "2031-03-07" {
		t.Fatalf("zero length is one day: %s", e.EndDate)
	}
	// Not midnight UTC: stored as timed, and counted.
	s.Start, s.End = at(6, 5), at(7, 5)
	e, n = mapOK(t, ev(s), nil)
	if e.AllDay.Known() || !n.AllDayUnaligned || e.StartDate != "" {
		t.Fatalf("%+v", e)
	}
}

func TestMapEventUnmapped(t *testing.T) {
	good := func() *hxbuild.Object { return hxbuild.NewEvent(baseSpec(1)) }
	cases := map[string]func() hxstore.Object{
		"layout": func() hxstore.Object { o := ev(baseSpec(1)); o.Tag = 0x456; return o },
		"short": func() hxstore.Object {
			o := ev(baseSpec(1))
			o.Raw = o.Raw[:100]
			return o
		},
		"area":   func() hxstore.Object { o := good(); o.PutU32(104, 1<<30); return obj(o) },
		"id":     func() hxstore.Object { o := good(); o.PutU32(820, 1<<30); return obj(o) },
		"no id":  func() hxstore.Object { o := good(); o.PutU32(824, 0); return obj(o) },
		"start":  func() hxstore.Object { o := good(); o.PutU64(584, 0); return obj(o) },
		"end":    func() hxstore.Object { o := good(); o.PutU64(592, 0); return obj(o) },
		"order":  func() hxstore.Object { o := good(); o.PutU64(592, hxbuild.Ticks(at(4, 0))); return obj(o) },
		"mod":    func() hxstore.Object { o := good(); o.PutU64(288, 0); return obj(o) },
		"string": func() hxstore.Object { o := good(); o.PutU32(1024, 1<<30); return obj(o) },
	}
	for name, mk := range cases {
		_, _, err := MapEvent("a", mk(), nil)
		var ue *UnmappedError
		if !errors.As(err, &ue) || ue.Reason == "" || !strings.Contains(err.Error(), ue.Reason) {
			t.Errorf("%s: %v", name, err)
		}
	}
	old := validate
	defer func() { validate = old }()
	validate = func(calendar.Event) error { return errors.New("refused") }
	if _, _, err := MapEvent("a", ev(baseSpec(1)), nil); err == nil {
		t.Fatal("the core's refusal must surface")
	}
}

func TestMapEventAttendees(t *testing.T) {
	// Nine attendees: the store's cap, so the list may be truncated, and it says so.
	s := baseSpec(1)
	s.Attendees = nil
	for i := 0; i < 9; i++ {
		s.Attendees = append(s.Attendees, hxbuild.Attendee{Name: "Fixture A", Address: "a@example.invalid", B: []uint32{0, 1, 2, 4, 7}[i%5]})
	}
	e, n := mapOK(t, ev(s), nil)
	var note map[string]any
	_ = json.Unmarshal([]byte(e.DetailRawJSON), &note)
	if !n.AttendeesAtCap || note["attendees_stored"] != float64(9) || note["attendees_maybe_truncated"] != true || n.AttendeeResponsesUnmapped != 1 {
		t.Fatalf("%+v %s", n, e.DetailRawJSON)
	}
	if !strings.Contains(e.AttendeesJSON, `"declined"`) {
		t.Fatal("declined response")
	}
	// A list of zero is not stated.
	s.Attendees = nil
	e, n = mapOK(t, ev(s), nil)
	if e.AttendeesJSON != "" || e.DetailRawJSON != "" || n.AttendeesUnparsed || !contains(calendar.UnknownFields(e), "attendees") {
		t.Fatalf("%+v", e)
	}
	// Damage: each makes the list unknown and is counted.
	base := func() *hxbuild.Object { return hxbuild.NewEvent(baseSpec(1)) }
	damaged := map[string]func(*hxbuild.Object){
		"bare string": func(o *hxbuild.Object) { o.PutU32(876, 1<<30) },
		"count":       func(o *hxbuild.Object) { o.PutU32(oEnd(o, 876), 1<<30) },
		"trailing":    func(o *hxbuild.Object) { o.Append([]byte{1, 2}) },
		"odd length":  func(o *hxbuild.Object) { o.PutU8(oEnd(o, 876)+4, 3) },
		"name out":    func(o *hxbuild.Object) { o.PutU8(oEnd(o, 876)+4, 250) },
	}
	for name, f := range damaged {
		o := base()
		f(o)
		e, n := mapOK(t, obj(o), nil)
		if e.AttendeesJSON != "" || !n.AttendeesUnparsed {
			t.Errorf("%s: %+v", name, n)
		}
	}
}

// oEnd returns the offset just past the terminator of the string whose word is at wordOff,
// for a test that patches the attendee list.
func oEnd(o *hxbuild.Object, wordOff int) int {
	raw := o.Encode()[4:]
	pos := 1109 + int(le32(raw, 104)) + int(le32(raw, wordOff))
	for ; ; pos += 2 {
		if raw[pos] == 0 && raw[pos+1] == 0 {
			return pos + 2
		}
	}
}

func le32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func TestMapEventDetail(t *testing.T) {
	// No detail object: the detail fields are unknown and it is counted.
	e, n := mapOK(t, ev(baseSpec(1)), nil)
	u := calendar.UnknownFields(e)
	if !n.DetailMissing || !contains(u, "join_url") || !contains(u, "dial_in") || !contains(u, "body") || !contains(u, "meeting_chat_id") {
		t.Fatalf("%v", u)
	}
	// A detail object with nothing in it: empty is stated, except the dial-in, whose
	// conference id is not located.
	empty := hxbuild.DetailSpec{Key: 501}
	e, n = mapOK(t, ev(baseSpec(1)), det(empty))
	u = calendar.UnknownFields(e)
	if n.DetailMissing || e.OnlineMeetingURL != "" || e.BodyHTML != "" || contains(u, "join_url") || contains(u, "body") || !contains(u, "dial_in") {
		t.Fatalf("%v %+v", u, e)
	}
	// A Teams join link yields the thread id.
	link := "https://example.invalid/l/meetup-join/19%3ameeting_FIXTUREabc%40thread.v2/0"
	e, _ = mapOK(t, ev(baseSpec(1)), det(hxbuild.DetailSpec{Key: 501, JoinLink: link, DialIn: "x"}))
	if e.TeamsThreadID != "19:meeting_FIXTUREabc@thread.v2" || e.OnlineMeetingURL != link {
		t.Fatalf("%q", e.TeamsThreadID)
	}
	// Damage: a field outside the object, a bad body.
	bad := map[string]func(*hxbuild.Object){
		"link": func(o *hxbuild.Object) { o.PutU32(700, 1<<30) },
		"dial": func(o *hxbuild.Object) { o.PutU32(728, 1<<30) },
		"body": func(o *hxbuild.Object) { o.PutU32(604, 1<<20) },
		"utf8": func(o *hxbuild.Object) {
			o.PutU32(604, 3|1<<31)
			o.PutU32(600, uint32(o.Len()-840-3)) //nolint:gosec // a small length
			o.Append([]byte{0xff, 0xfe})
		},
		"area":   func(o *hxbuild.Object) { o.PutU32(104, 1<<30) },
		"layout": func(o *hxbuild.Object) { o.PutU32(104, 0) },
	}
	for name, f := range bad {
		o := hxbuild.NewDetail(baseDetail(1))
		f(o)
		d := obj(o)
		if name == "layout" {
			d.Tag = 1
		}
		e, n := mapOK(t, ev(baseSpec(1)), &d)
		if !n.DetailUnreadable || e.OnlineMeetingURL != "" || !contains(calendar.UnknownFields(e), "join_url") {
			t.Errorf("%s: %+v", name, n)
		}
	}
	// A record whose words are cut off by the end of the object.
	cut := ev(baseSpec(1))
	cut.Raw = cut.Raw[:len(cut.Raw)-6]
	if e, n := mapOK(t, cut, nil); e.AttendeesJSON != "" || !n.AttendeesUnparsed {
		t.Fatal("cut record")
	}
	// A short detail object cannot hold its own length words.
	short := det(baseDetail(1))
	short.Raw = short.Raw[:600]
	if _, n := mapOK(t, ev(baseSpec(1)), short); !n.DetailUnreadable {
		t.Fatal("short detail")
	}
}

func TestMapEventScrubsCredentials(t *testing.T) {
	s := baseSpec(1)
	s.Subject = "Fixture password=hunter2hunter2 https://example.invalid/?token=abcdefghijklmnopqrstuvwxyz0123456789"
	s.Subject = "Fixture " + strings.Repeat("A", 8) + " bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop"
	e, n := mapOK(t, ev(s), nil)
	if n.Redacted == 0 || strings.Contains(e.Subject, "eyJhbGci") {
		t.Fatalf("%q %d", e.Subject, n.Redacted)
	}
}

func TestHelpersOnBadObjects(t *testing.T) {
	short := hxstore.Object{Raw: make([]byte, 10)}
	if HasID(short) {
		t.Fatal("short object has no id")
	}
	if _, ok := DetailKey(short); ok {
		t.Fatal("detail key")
	}
	if _, ok := detailLink(short); ok {
		t.Fatal("link")
	}
	if flagBit(short, 1082, 1) != calendar.TriUnknown {
		t.Fatal("flag out of range")
	}
}

// TestIDIsUTF16HexText pins the stored form of the id against the Teams key form: the object
// holds the upper-case hex text of the UID bytes in UTF-16LE with a terminator, and the
// mapped ICalUID is that text, character for character, as a Teams iCalUID.
func TestIDIsUTF16HexText(t *testing.T) {
	spec := baseSpec(1)
	raw := ev(spec).Raw
	off := int(le32(raw, 820))
	n := int(le32(raw, 824))
	if n != 226 {
		t.Fatalf("a 56-byte id is stored in %d bytes, want 226", n)
	}
	stored := raw[1109+off : 1109+off+n]
	if stored[0] != '0' || stored[1] != 0 || stored[2] != '4' || stored[3] != 0 || stored[n-2] != 0 || stored[n-1] != 0 {
		t.Fatalf("stored form %x", stored[:8])
	}
	e, _ := mapOK(t, ev(spec), nil)
	// The Teams fixture writes the same UID as: prefix, date, hex of FIXTURE-label padded to 72.
	const want = "040000008200E00074C5B7101A82E008" + "00000000" + "0000000000000000" + "0000000000000000" + "10000000" +
		"46495854555245" + "2D544553542D303031" // FIXTURE-TEST-001
	if e.ICalUID != want || e.SourceID != want || e.GlobalID != want || len(e.ICalUID) != 112 {
		t.Fatalf("%s", e.ICalUID)
	}
	// Lower-case digits in the store are upper-cased.
	o := hxbuild.NewEvent(spec)
	lower := hxbuild.UTF16Z(strings.ToLower(want))
	o.PutU32(824, uint32(len(lower))) //nolint:gosec // small
	putBytes(o, 1109+off, lower)
	if e, _, err := MapEvent("a", obj(o), nil); err != nil || e.ICalUID != want {
		t.Fatalf("%v", err)
	}
}

func TestMapEventRejectsBadIDText(t *testing.T) {
	for name, text := range map[string][]byte{
		"not hex":       hxbuild.UTF16Z("04ZZ"),
		"odd digits":    hxbuild.UTF16Z("040"),
		"no terminator": hxbuild.UTF16Z("0400")[:8],
		"odd length":    append(hxbuild.UTF16Z("0400"), 0),
		"raw bytes":     {4, 0, 0, 0, 0x82, 0, 0xe0, 0, 0, 0},
		"too short":     {0, 0},
	} {
		o := hxbuild.NewEvent(baseSpec(1))
		off := int(le32(o.Encode()[4:], 820))
		o.PutU32(824, uint32(len(text))) //nolint:gosec // small
		putBytes(o, 1109+off, text)
		_, _, err := MapEvent("a", obj(o), nil)
		var ue *UnmappedError
		if !errors.As(err, &ue) || ue.Reason != "id is not UTF-16 hex text" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// putBytes writes b into the object at off, within its current length.
func putBytes(o *hxbuild.Object, off int, b []byte) {
	for i, v := range b {
		o.PutU8(off+i, v)
	}
}
