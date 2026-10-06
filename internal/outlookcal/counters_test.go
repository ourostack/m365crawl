package outlookcal

import (
	"strings"
	"testing"

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
	cases := map[string]struct {
		o                *hxbuild.Object
		end, zero, other bool
	}{
		"end":   {o: func() *hxbuild.Object { o := base(true); o.Append([]byte{1, 2}); return o }(), end: true},
		"zero":  {o: func() *hxbuild.Object { o := base(false); o.Append([]byte{1, 2}); return o }(), zero: true},
		"other": {o: func() *hxbuild.Object { o := base(true); o.PutU32(oEnd(o, 876), 1<<30); return o }(), other: true},
	}
	for name, c := range cases {
		_, n := mapOK(t, obj(c.o), nil)
		if !n.AttendeesUnparsed || n.AttendeesEndMismatch != c.end || n.AttendeesCountZero != c.zero || n.AttendeesOtherUnparsed != c.other {
			t.Errorf("%s: %+v", name, n)
		}
	}
	// Collect adds them, and they sum to the unparsed count.
	s := collect(t, storeOf(t, framed(cases["end"].o)), Options{})
	if s.Notes.AttendeesUnparsed != 1 || s.Notes.AttendeesEndMismatch != 1 || s.Notes.AttendeesCountZero != 0 || s.Notes.AttendeesOtherUnparsed != 0 {
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
