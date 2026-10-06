package calendar

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateEventRefusesUndeclaredUnknown(t *testing.T) {
	e := thin(t, t1)
	e.UnknownDeclared = false
	var bad *InvalidEventError
	if _, err := Capture(nil, e); !errors.As(err, &bad) || !strings.Contains(err.Error(), "UnknownDeclared") {
		t.Fatalf("an event whose mapper did not declare Unknown is refused: %v", err)
	}
	// Through a snapshot it is counted as refused and stores nothing.
	db := openDB(t)
	counts := snap(t, db, SourceTeams, acctTeams, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z", twin(t, SourceTeams, "2026-11-02T08:00:00Z", func(e *Event) { e.UnknownDeclared = false }))
	if len(counts.Refused) != 1 || count(t, db, `SELECT count(*) FROM calendar_source_events`) != 0 {
		t.Fatalf("%+v", counts)
	}
}

func TestStoredRowDoesNotCarryTheDeclaration(t *testing.T) {
	if got := mustCapture(t, nil, thin(t, t1)); got.UnknownDeclared {
		t.Fatal("UnknownDeclared is input only")
	}
}

func TestValidateEventRefusesAZeroStartOrEnd(t *testing.T) {
	s, e := mustTime(t, "2026-10-05T00:00:00Z"), mustTime(t, "2026-10-06T00:00:00Z")
	for name, ev := range map[string]Event{
		"all-day, zero start, end set": {AllDay: TriTrue, StartDate: "2026-10-05", End: e},
		"all-day, start set, zero end": {AllDay: TriTrue, StartDate: "2026-10-05", Start: s},
		"all-day, both zero":           {AllDay: TriTrue, StartDate: "2026-10-05"},
		"timed, zero start":            {End: e},
		"timed, zero end":              {Start: s},
		"unknown flag, zero end":       {AllDay: TriUnknown, Start: s},
	} {
		ev.UnknownDeclared, ev.Source, ev.SourceID = true, SourceTeams, "d"
		if err := ValidateEvent(ev); err == nil || !strings.Contains(err.Error(), "zero") {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := Event{UnknownDeclared: true, Source: SourceTeams, SourceID: "d", AllDay: TriTrue, StartDate: "2026-10-05", Start: s, End: e}
	if err := ValidateEvent(ok); err != nil {
		t.Fatal(err)
	}
}

func TestEqualTimeCopiesWithDifferentStatedStatusStoreOneRow(t *testing.T) {
	// Two copies at one time carry the same join link. One states the dial-in as known empty, the
	// other lists it unknown. The byte-equal values tie, so the stated status must decide.
	a, b := thin(t, t1), thin(t, t1)
	a.OnlineMeetingURL, b.OnlineMeetingURL = "https://teams.example.test/l/x", "https://teams.example.test/l/x"
	a.Unknown = without(a.Unknown, FieldJoinURL, FieldDialIn, FieldShortJoinURL, FieldMeetingChatID)
	b.Unknown = without(b.Unknown, FieldJoinURL, FieldShortJoinURL, FieldMeetingChatID)
	b.Unknown = append(b.Unknown, FieldDialIn)
	got := converge(t, "tie", []Event{a, b})
	if got.unknown(FieldDialIn) {
		t.Fatalf("the copy that stated more wins: %v", got.Unknown)
	}
}

// recorder is a testing.TB that records failures instead of failing the real test.
type recorder struct {
	testing.TB
	failures []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, format)
}

func TestAssertEveryFieldClassified(t *testing.T) {
	// A rich copy states or lists everything it carries; the thin copy's detail group is listed.
	ok := &recorder{TB: t}
	e := thin(t, t1)
	e.ReminderMinutes = nil
	AssertEveryFieldClassified(ok, e, FieldAttendees)
	if len(ok.failures) != 0 {
		t.Fatalf("a classified event passes: %v", ok.failures)
	}
	// A field that is neither stated nor listed fails, and so does a missing declaration.
	e.Unknown = without(e.Unknown, FieldCategories)
	e.UnknownDeclared = false
	bad := &recorder{TB: t}
	AssertEveryFieldClassified(bad, e)
	if len(bad.failures) < 2 {
		t.Fatalf("an unclassified field and a missing declaration both fail: %v", bad.failures)
	}
}
