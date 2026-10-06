package outlookcal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

func TestCancelledSubjectCounters(t *testing.T) {
	s := baseSpec(1)
	s.Cancelled = true
	s.Subject, s.SubjectBare = "Canceled: Fixture", "Fixture"
	if _, n := mapOK(t, ev(s), nil); !n.CancelledSubjectsDiffer || n.CancelledSubjectsMatch {
		t.Fatalf("%+v", n)
	}
	s.Subject = s.SubjectBare
	if _, n := mapOK(t, ev(s), nil); n.CancelledSubjectsDiffer || !n.CancelledSubjectsMatch {
		t.Fatalf("%+v", n)
	}
	s.Cancelled = false
	if _, n := mapOK(t, ev(s), nil); n.CancelledSubjectsDiffer || n.CancelledSubjectsMatch {
		t.Fatalf("not cancelled: %+v", n)
	}
}

func TestOccurrenceNoDate(t *testing.T) {
	s := baseSpec(1)
	for _, typ := range []uint32{1, 2} {
		s.EventType = typ
		if _, n := mapOK(t, ev(s), nil); !n.OccurrenceNoDate {
			t.Errorf("type %d: id without a date is counted", typ)
		}
	}
	s.ID = hxbuild.GlobalObjectID(2031, 3, 5, "FIXTURE-DATED")
	if _, n := mapOK(t, ev(s), nil); n.OccurrenceNoDate {
		t.Error("a dated occurrence is not counted")
	}
	s.ID, s.EventType = hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-SINGLE"), 0
	if _, n := mapOK(t, ev(s), nil); n.OccurrenceNoDate {
		t.Error("a single event needs no date")
	}
}

func TestSeriesKeyCrossCheck(t *testing.T) {
	mk := func(day int, label string, key uint64) *hxbuild.Object {
		s := baseSpec(1)
		s.ID = hxbuild.GlobalObjectID(2031, 3, day, label)
		s.SeriesKey = key
		s.DetailKey = 900 + uint32(day) //nolint:gosec // a small day number
		return hxbuild.NewEvent(s)
	}
	// Agreement: two dates of one series share a key.
	ok := collect(t, storeOf(t, framed(mk(5, "FIXTURE-A", 7), mk(6, "FIXTURE-A", 7))), Options{})
	if ok.Notes.SeriesKeySplits != 0 || ok.Notes.SeriesGroupSplits != 0 {
		t.Fatalf("%+v", ok.Notes)
	}
	// One key, two series by id.
	a := collect(t, storeOf(t, framed(mk(5, "FIXTURE-A", 7), mk(5, "FIXTURE-B", 7))), Options{})
	if a.Notes.SeriesKeySplits != 1 || a.Notes.SeriesGroupSplits != 0 {
		t.Fatalf("%+v", a.Notes)
	}
	// One series by id, two keys.
	b := collect(t, storeOf(t, framed(mk(5, "FIXTURE-A", 7), mk(6, "FIXTURE-A", 8))), Options{})
	if b.Notes.SeriesKeySplits != 0 || b.Notes.SeriesGroupSplits != 1 {
		t.Fatalf("%+v", b.Notes)
	}
}

func TestDetailCopiesDiffer(t *testing.T) {
	one := baseDetail(1)
	other := one
	other.BodyHTML = "<p>Fixture edited</p>"
	same := collect(t, storeOf(t, framed(hxbuild.NewEvent(baseSpec(1)), hxbuild.NewDetail(one), hxbuild.NewDetail(one))), Options{})
	if same.Notes.DetailCopiesDiffer != 0 {
		t.Fatalf("identical copies: %+v", same.Notes)
	}
	diff := collect(t, storeOf(t, framed(hxbuild.NewEvent(baseSpec(1)), hxbuild.NewDetail(one), hxbuild.NewDetail(other), hxbuild.NewDetail(one))), Options{})
	if diff.Notes.DetailCopiesDiffer != 1 || diff.Events[0].BodyHTML != "<p>Fixture body</p>" {
		t.Fatalf("%+v", diff.Notes)
	}
}

func TestBodyNULTrimmed(t *testing.T) {
	d := baseDetail(1)
	d.BodyHTML += "\x00"
	e, n := mapOK(t, ev(baseSpec(1)), det(d))
	if !n.BodyNULTrimmed || e.BodyHTML != "<p>Fixture body</p>" {
		t.Fatalf("%q %+v", e.BodyHTML, n)
	}
	e, n = mapOK(t, ev(baseSpec(1)), det(baseDetail(1)))
	if n.BodyNULTrimmed || e.BodyHTML != "<p>Fixture body</p>" {
		t.Fatalf("%+v", n)
	}
	// Only one NUL goes.
	d.BodyHTML += "\x00"
	if e, _ = mapOK(t, ev(baseSpec(1)), det(d)); e.BodyHTML != "<p>Fixture body</p>\x00" {
		t.Fatalf("%q", e.BodyHTML)
	}
}

func TestUnknownZoneNamesAreBounded(t *testing.T) {
	for name, keep := range map[string]bool{
		"Fixture Unknown Time":                true,
		strings.Repeat("Z", MaxZoneNameLen):   true,
		strings.Repeat("Z", MaxZoneNameLen+1): false,
		"Fixture é Zone":                      false,
		"Fixture\tZone":                       false,
		"Fixture \u007f Zone":                 false,
	} {
		s := baseSpec(1)
		s.ZoneName = name
		e, n := mapOK(t, ev(s), nil)
		if keep != (n.UnknownZone == name) || keep == n.UnknownZoneRejected {
			t.Errorf("%q: %+v", name, n)
		}
		if e.TimeZoneIANA != "" {
			t.Errorf("%q resolved", name)
		}
	}
	// A rejected name is counted by Collect and kept nowhere.
	s := baseSpec(1)
	s.ZoneName = strings.Repeat("Z", 200)
	r := collect(t, storeOf(t, framed(hxbuild.NewEvent(s))), Options{})
	if r.Notes.UnknownZonesRejected != 1 || len(r.Notes.UnknownZones) != 0 {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestAttendeeFailureCauses(t *testing.T) {
	base := func(attendees bool) *hxbuild.Object {
		s := baseSpec(1)
		if !attendees {
			s.Attendees = nil
		}
		return hxbuild.NewEvent(s)
	}
	cut := func(o *hxbuild.Object, keep func(end int) int) hxstore.Object {
		e := obj(o)
		e.Raw = e.Raw[:keep(oEnd(o, 876))]
		return e
	}
	cases := map[string]struct {
		o     hxstore.Object
		cause string
	}{
		"end":     {obj(func() *hxbuild.Object { o := base(true); o.Append([]byte{1, 2}); return o }()), "end_mismatch"},
		"zero":    {obj(func() *hxbuild.Object { o := base(false); o.Append([]byte{1, 2}); return o }()), "count_zero"},
		"bare":    {obj(func() *hxbuild.Object { o := base(true); o.PutU32(876, 1<<30); return o }()), "bare_string_end"},
		"outside": {cut(base(true), func(end int) int { return end + 2 }), "count_outside"},
		"big": {obj(func() *hxbuild.Object {
			o := base(true)
			o.PutU32(oEnd(o, 876), 1<<30)
			o.Append([]byte{1, 2})
			return o
		}()), "count_too_big"},
		// A count of one over a record cut after its name: the address length is past the end.
		"missing": {cut(func() *hxbuild.Object { o := base(true); o.PutU32(oEnd(o, 876), 1); return o }(), func(end int) int { return end + 4 + 1 + 22 }), "length_missing"},
		"nobytes": {cut(func() *hxbuild.Object { o := base(true); o.PutU32(oEnd(o, 876), 1<<30); return o }(), func(end int) int { return end + 4 }), "count_too_big"},
		"odd":     {obj(func() *hxbuild.Object { o := base(true); o.PutU8(oEnd(o, 876)+4, 3); return o }()), "length_odd"},
		"text":    {obj(func() *hxbuild.Object { o := base(true); o.PutU8(oEnd(o, 876)+4, 250); return o }()), "text_outside"},
		"words":   {cut(base(true), func(int) int { return len(obj(base(true)).Raw) - 6 }), "words_cut"},
	}
	for name, c := range cases {
		_, n := mapOK(t, c.o, nil)
		other := c.cause != "end_mismatch" && c.cause != "count_zero"
		if !n.AttendeesUnparsed || n.AttendeeFailure != c.cause || n.AttendeesEndMismatch != (c.cause == "end_mismatch") ||
			n.AttendeesCountZero != (c.cause == "count_zero") || n.AttendeesOtherUnparsed != other {
			t.Errorf("%s: %+v", name, n)
		}
	}
	// Every named cause is exercised, and an intact list names none.
	if _, n := mapOK(t, obj(base(true)), nil); n.AttendeeFailure != "" || n.AttendeesUnparsed {
		t.Fatalf("%+v", n)
	}
	for _, name := range attNames {
		found := false
		for _, c := range cases {
			found = found || c.cause == name
		}
		if !found {
			t.Errorf("cause %s has no case", name)
		}
	}
	// Collect adds them, and they sum to the unparsed count.
	endO := base(true)
	endO.Append([]byte{1, 2})
	bigSpec := baseSpec(2)
	bigSpec.ID = hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-BIG")
	bigO2 := hxbuild.NewEvent(bigSpec)
	bigO2.PutU32(oEnd(bigO2, 876), 1<<30)
	bigO2.Append([]byte{1, 2})
	s := collect(t, storeOf(t, framed(endO, bigO2)), Options{})
	if s.Notes.AttendeesUnparsed != 2 || s.Notes.AttendeesEndMismatch != 1 || s.Notes.AttendeesOtherUnparsed != 1 ||
		s.Notes.AttendeeFailures["end_mismatch"] != 1 || s.Notes.AttendeeFailures["count_too_big"] != 1 || len(s.Notes.AttendeeFailures) != 2 {
		t.Fatalf("%+v", s.Notes)
	}
	// The cancelled and body counters reach the result too.
	cs := baseSpec(2)
	cs.Cancelled, cs.SubjectBare = true, "Other"
	d := baseDetail(2)
	d.BodyHTML += "\x00"
	r := collect(t, storeOf(t, framed(hxbuild.NewEvent(cs), hxbuild.NewDetail(d))), Options{})
	if r.Notes.CancelledSubjectsDiffer != 1 || r.Notes.BodyNULTrimmed != 1 {
		t.Fatalf("%+v", r.Notes)
	}
	os := baseSpec(3)
	os.EventType = 1
	if r = collect(t, storeOf(t, framed(hxbuild.NewEvent(os))), Options{}); r.Notes.OccurrenceNoDate != 1 || r.Notes.CancelledSubjectsMatch != 0 {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestGreedyAttendeesWhenCountExceedsStored(t *testing.T) {
	// The count word says 20 and the store holds the records of a capped list: the records
	// are accepted, the note says the list may be short, and Collect buckets them.
	mk := func(label string, records int) *hxbuild.Object {
		s := baseSpec(1)
		s.ID = hxbuild.GlobalObjectID(0, 0, 0, label)
		s.Attendees = nil
		for i := 0; i < records; i++ {
			s.Attendees = append(s.Attendees, hxbuild.Attendee{Name: "Fixture Person", Address: "p@example.invalid", B: 0})
		}
		o := hxbuild.NewEvent(s)
		o.PutU32(oEnd(o, 876), 20)
		return o
	}
	e, n := mapOK(t, obj(mk("FIXTURE-G", 3)), nil)
	var note map[string]any
	_ = json.Unmarshal([]byte(e.DetailRawJSON), &note)
	if !n.AttendeesCountExceedsStored || n.AttendeesStored != 3 || n.AttendeesUnparsed || note["attendees_stored"] != float64(3) || note["attendees_maybe_truncated"] != true {
		t.Fatalf("%+v %s", n, e.DetailRawJSON)
	}
	if strings.Count(e.AttendeesJSON, `"accepted"`) != 3 {
		t.Fatal(e.AttendeesJSON)
	}
	var objs []*hxbuild.Object
	for i, c := range []int{3, 7, 8, 9, 10, 12} {
		objs = append(objs, mk("FIXTURE-G"+string(rune('A'+i)), c))
	}
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	want := map[string]int{"1-7": 2, "8": 1, "9": 1, "10+": 2}
	if r.Notes.AttendeesCountExceedsStored != 6 || r.Notes.AttendeesUnparsed != 0 || len(r.Notes.ExceedsStoredRecords) != 4 {
		t.Fatalf("%+v", r.Notes)
	}
	for k, v := range want {
		if r.Notes.ExceedsStoredRecords[k] != v {
			t.Errorf("%s: %+v", k, r.Notes.ExceedsStoredRecords)
		}
	}
}

func TestOddLengthDiagnostic(t *testing.T) {
	// A list whose lengths count characters: odd lengths fail the byte reading, and the
	// diagnostic says it would parse as characters. The mapping stays unknown.
	build := func(chars bool) hxstore.Object {
		o := hxbuild.NewEvent(baseSpec(1))
		end := oEnd(o, 876)
		raw := obj(o).Raw[:end]
		out := append([]byte{}, raw...)
		out = append(out, 1, 0, 0, 0) // one record
		put := func(text string) {
			u := hxbuild.UTF16Z(text)
			u = u[:len(u)-2]
			if chars {
				out = append(out, byte(len(text)))
			} else {
				out = append(out, byte(len(u)))
			}
			out = append(out, u...)
		}
		put("Fixture")
		put("a@example.invalid")
		out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		e := obj(o)
		e.Raw = out
		return e
	}
	_, n := mapOK(t, build(true), nil)
	if !n.AttendeesUnparsed || n.AttendeeFailure != "length_odd" || !n.AttendeesOddLengthCharsParse {
		t.Fatalf("%+v", n)
	}
	// Odd and still wrong as characters: not counted.
	o := build(true)
	o.Raw = append(o.Raw, 9)
	if _, n = mapOK(t, o, nil); !n.AttendeesUnparsed || n.AttendeesOddLengthCharsParse {
		t.Fatalf("%+v", n)
	}
}
