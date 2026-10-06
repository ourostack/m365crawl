package outlookcal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
	"github.com/ourostack/teamscrawl/internal/outlookdesktop"
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

func TestAttendeeListFollowsTheLastString(t *testing.T) {
	// Another string (+980, or +772) sits after the bare subject: the list starts after
	// the extra string, and that string's text is not mapped.
	for _, word := range []int{980, 772} {
		for _, extra := range []string{"", "Fixture extra text", "x"} {
			s := baseSpec(1)
			s.ExtraWord, s.ExtraString = word, extra
			e, n := mapOK(t, ev(s), nil)
			if n.AttendeesUnparsed || n.AttendeeFailure != "" || strings.Count(e.AttendeesJSON, `"name"`) != 2 {
				t.Errorf("%d %q: %+v %s", word, extra, n, e.AttendeesJSON)
			}
			if len(extra) > 1 && strings.Contains(fmt.Sprintf("%+v", e), extra) {
				t.Errorf("%d: the unidentified string is mapped", word)
			}
		}
	}
	// An extra string word that points outside the object is ignored, a known one is not.
	s := baseSpec(1)
	o := hxbuild.NewEvent(s)
	o.PutU32(980, 1<<30)
	if e, _ := mapOK(t, obj(o), nil); strings.Count(e.AttendeesJSON, `"name"`) != 2 {
		t.Error("a bad unidentified word must not hide the list")
	}
	// A known string word (+1024) that points past the start of the list changes nothing:
	// the list still starts where the bare subject ends. A maximum-end rule failed here.
	o = hxbuild.NewEvent(s)
	endAt := len(obj(o).Raw) - 14 - 1109 - 812 // inside the last record, as an offset from T
	o.PutU32(1024, uint32(endAt))              //nolint:gosec // a small test offset
	o.PutU32(1028, 2)
	if e, n := mapOK(t, obj(o), nil); n.AttendeesUnparsed || strings.Count(e.AttendeesJSON, `"name"`) != 2 {
		t.Errorf("%+v", n)
	}
	// An extra string with no terminator before the object ends: no list start.
	o = hxbuild.NewEvent(s)
	end := oEnd(o, 876)
	o.PutU32(980, uint32(end-1109-812)) //nolint:gosec // a small test offset
	cut := obj(o)
	cut.Raw = cut.Raw[:end]
	if _, n := mapOK(t, cut, nil); n.AttendeeFailure != "bare_string_end" {
		t.Errorf("%+v", n)
	}
	o = hxbuild.NewEvent(s)
	o.PutU32(876, 1<<30)
	if _, n := mapOK(t, obj(o), nil); n.AttendeeFailure != "bare_string_end" {
		t.Errorf("%+v", n)
	}
}

func TestOddLengthStaysUnparsed(t *testing.T) {
	// A list whose lengths count characters is not read as one: it stays unknown.
	o := hxbuild.NewEvent(baseSpec(1))
	e := obj(o)
	out := append([]byte{}, e.Raw[:oEnd(o, 876)]...)
	out = append(out, 1, 0, 0, 0, 7) // one record, a name length of 7
	out = append(out, hxbuild.UTF16Z("Fixture")...)
	e.Raw = out
	if _, n := mapOK(t, e, nil); n.AttendeeFailure != "length_odd" || !n.AttendeesUnparsed {
		t.Fatalf("%+v", n)
	}
}

func TestBodyNULCountsOncePerDetailObject(t *testing.T) {
	d := baseDetail(1)
	d.BodyHTML += "\x00"
	a, b := baseSpec(1), baseSpec(1)
	b.ID = hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-SHARED")
	r := collect(t, storeOf(t, framed(hxbuild.NewEvent(a), hxbuild.NewEvent(b), hxbuild.NewDetail(d))), Options{})
	if len(r.Events) != 2 || r.Notes.BodyNULTrimmed != 1 {
		t.Fatalf("%d events, %+v", len(r.Events), r.Notes)
	}
}

func TestUnknownZonesAreCapped(t *testing.T) {
	var objs []*hxbuild.Object
	for i := 0; i < MaxUnknownZones+5; i++ {
		s := baseSpec(1)
		s.ID = hxbuild.GlobalObjectID(0, 0, 0, fmt.Sprintf("FIXTURE-Z%02d", i))
		s.ZoneName = fmt.Sprintf("Fixture Zone %02d", i)
		objs = append(objs, hxbuild.NewEvent(s))
	}
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Notes.UnknownZones) != MaxUnknownZones || r.Notes.UnknownZonesOver != 5 || r.Notes.UnknownZones[0] != "Fixture Zone 00" {
		t.Fatalf("%d kept, %d over", len(r.Notes.UnknownZones), r.Notes.UnknownZonesOver)
	}
}

// mappedDigest is a hash of the derived events of the committed fixture.
func mappedDigest(t *testing.T) string {
	t.Helper()
	res := collect(t, fixtureStore(t, "HxStore.hxd"), Options{})
	data, err := json.Marshal(res.Events)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pinnedMapping ties MapperVersion to the output it describes. A mapping change alters
// the digest, which fails this test until MapperVersion is raised (so a stored event is
// derived again) and the pair below is updated together. A change of the fixture alone moves the
// digest and not the version: the mapper is the same.
var pinnedMapping = struct {
	version int
	digest  string
}{version: 4, digest: "bfd947411e7b47ed371e393ef42217b68f3ca0c22dc9e60972f5b54a5a117ae7"}

func TestMapperVersionIsPinnedToTheMappedOutput(t *testing.T) {
	got := mappedDigest(t)
	if got != pinnedMapping.digest && MapperVersion == pinnedMapping.version {
		t.Fatalf("the mapped output changed: raise MapperVersion and set the pin to {%d, %q}", MapperVersion+1, got)
	}
	if got == pinnedMapping.digest && MapperVersion != pinnedMapping.version {
		t.Fatalf("MapperVersion is %d but the pin says %d for the same output", MapperVersion, pinnedMapping.version)
	}
	if MapperVersion != pinnedMapping.version || got != pinnedMapping.digest {
		t.Fatalf("update the pin to {%d, %q}", MapperVersion, got)
	}
	// The version feeds the store fingerprint, so a bump reads an unchanged store again.
	at := time.Unix(1, 0)
	v := outlookdesktop.Versions{Store: "i", Reader: 1, Mapper: MapperVersion, Rules: 1}
	w := v
	w.Mapper++
	if outlookdesktop.FingerprintFor(10, at, v) == outlookdesktop.FingerprintFor(10, at, w) {
		t.Fatal("the mapper version does not move the fingerprint")
	}
}
