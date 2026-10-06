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

func TestValidateEventRefusesAllDayWithoutInstants(t *testing.T) {
	e := Event{UnknownDeclared: true, Source: SourceTeams, SourceID: "d", AllDay: TriTrue, StartDate: "2026-10-05"}
	if err := ValidateEvent(e); err == nil || !strings.Contains(err.Error(), "instants") {
		t.Fatalf("%v", err)
	}
	e.Start = mustTime(t, "2026-10-05T00:00:00Z")
	if err := ValidateEvent(e); err != nil {
		t.Fatal(err)
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
