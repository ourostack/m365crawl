package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

func nativeAttempt(t *testing.T, s *Store) KnowledgeAttempt {
	t.Helper()
	a, err := s.BeginAttendeeAttempt(context.Background(), KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: attendeeFixtureOrigin, At: base}, AttendeeRequest{Root: attendeeFixtureRoot, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAttendeeAttemptFailureAndRetry(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := nativeAttempt(t, s)
	request, err := s.AttendeeRequestForLocator(ctx, a.LocatorID)
	if err != nil || request.Root != attendeeFixtureRoot || !request.Discover || request.SourceID != 0 {
		t.Fatalf("retry changed request: %#v %v", request, err)
	}
	if _, err := s.AttendeeRequestForLocator(ctx, 999); err == nil {
		t.Fatal("unknown request accepted")
	}
	if err := s.RecordAttendeeFailure(ctx, a, KnowledgeOutcome{Status: "complete", At: base}); !errors.Is(err, outlookmeetingattendees.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.RecordAttendeeFailure(ctx, a, KnowledgeOutcome{Status: "failed", Reason: "synthetic_failure", At: base}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AttendeeSources(ctx, 0)
	if err != nil || len(got.Coverage.Sources) != 1 || got.Coverage.Sources[0].SourceID != nil || got.Coverage.Sources[0].Status != "failed" || len(got.Rows) != 0 {
		t.Fatalf("failed registration invented a source: %#v %v", got, err)
	}
	if _, err := s.AttendeeSources(ctx, 999); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal(err)
	}
	if err := s.RecordAttendeeFailure(ctx, a, KnowledgeOutcome{Status: "cancelled", At: base}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatal("terminal attempt rewritten")
	}
}

func TestAttendeeAttemptFaultSweeps(t *testing.T) {
	sweepFaults(t, func(*testing.T, *Store) {}, func(ctx context.Context, s *Store) error {
		_, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: attendeeFixtureOrigin, At: base}, AttendeeRequest{Root: attendeeFixtureRoot, Discover: true})
		return err
	})
	var a KnowledgeAttempt
	setup := func(t *testing.T, s *Store) { a = nativeAttempt(t, s) }
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		return s.RecordAttendeeFailure(ctx, a, KnowledgeOutcome{Status: "failed", At: base})
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.AttendeeRequestForLocator(ctx, a.LocatorID)
		return err
	})
}

func TestAttendeeAdmissionInputGuards(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	a := nativeAttempt(t, s)
	good := AttendeeCapture{Evidence: attendeeModel(t, "B", 4), InputDigest: strings.Repeat("a", 64), CalendarReadID: 1, AcquiredAt: base, AdmittedAt: base}
	for _, edit := range []func(*AttendeeCapture){
		func(c *AttendeeCapture) { c.AcquiredAt = time.Time{} },
		func(c *AttendeeCapture) { c.AdmittedAt = time.Time{} },
		func(c *AttendeeCapture) { c.CalendarReadID = 0 },
		func(c *AttendeeCapture) { c.InputDigest = "invalid" },
		func(c *AttendeeCapture) { c.Evidence.AmbiguousKeys = []uint32{999} },
		func(c *AttendeeCapture) { c.Census.Objects = -1 },
	} {
		c := good
		edit(&c)
		if err := s.CommitAttendeeCapture(ctx, a, c); err == nil {
			t.Fatal("invalid admission succeeded")
		}
	}
	observed := time.Time{}
	good.SourceObservedAt = &observed
	if err := s.CommitAttendeeCapture(ctx, a, good); err != nil {
		t.Fatal(err)
	}
	got, err := s.AttendeeSources(ctx, 0)
	if err != nil || got.Coverage.Sources[0].SourceObservedAt == nil || !got.Coverage.Sources[0].SourceObservedAt.IsZero() {
		t.Fatal("known zero source clock became unknown")
	}
	for _, input := range []KnowledgeInput{
		{Kind: "wrong", Locator: attendeeFixtureOrigin, Platform: "fixture", At: base},
		{Kind: AttendeeKind, Locator: "relative/HxStore.hxd", Platform: "fixture", At: base},
		{Kind: AttendeeKind, Locator: attendeeFixtureOrigin, Platform: string([]byte{255}), At: base},
		{Kind: AttendeeKind, Locator: attendeeFixtureOrigin, Platform: strings.Repeat("x", 262145), At: base},
		{Kind: AttendeeKind, Locator: attendeeFixtureOrigin, Platform: "fixture", At: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if _, err := s.BeginAttendeeAttempt(ctx, input, AttendeeRequest{}); err == nil {
			t.Fatal("invalid request succeeded")
		}
	}
	if attendeeStrings(strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), "x") {
		t.Fatal("scope total overflow")
	}
	if _, err := s.AttendeeDetail(ctx, nil, 0); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal(err)
	}
	if _, err := s.AttendeeEventEvidence(ctx, nil, "missing", AttendeeFilter{Limit: 0}); !errors.Is(err, outlookmeetingattendees.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.AttendeeEventEvidence(ctx, nil, "missing", AttendeeFilter{Limit: 1}); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal(err)
	}
}
