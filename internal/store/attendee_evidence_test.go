package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

var (
	attendeeFixtureRoot   = filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "synthetic", "native")
	attendeeFixtureOrigin = filepath.Join(attendeeFixtureRoot, "Main", "HxStore.hxd")
)

func attendeeModel(t *testing.T, label string, key uint32) outlookmeetingattendees.Evidence {
	t.Helper()
	name := label
	native := outlookattendees.Result{
		Attendees: []outlookattendees.Attendee{{Key: 11, DetailKey: key, VersionRaw: 1, BlockOffset: 64, PayloadPos: 15, Name: &name}},
		Evidence:  []outlookattendees.Evidence{{DetailKey: key, DetailCopies: 1, EventLinks: 1}},
	}
	e, _, err := outlookmeetingattendees.Build(context.Background(), native, outlookcal.AttendeeSelections{Rows: []outlookcal.SelectionReceipt{
		{SourceID: "AA11", DetailKey: key, DetailCopies: 1, DetailPresent: true, BlockOffset: 64, PayloadPos: 15},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func admitAttendeeFixture(t *testing.T, s *Store, e outlookmeetingattendees.Evidence, at time.Time, events ...calendar.Event) KnowledgeAttempt {
	t.Helper()
	ctx := context.Background()
	if len(events) == 0 {
		events = []calendar.Event{outlookEvent(calendar.TriFalse, base)}
	}
	cal, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Origin: attendeeFixtureOrigin, At: at, FreshAt: base, Zone: time.UTC, Stamp: "same", Events: events},
		func(CalendarResult) Run {
			return Run{StartedAt: at, FinishedAt: at, Source: "outlook|Main", Fingerprint: "synthetic", Status: "ok"}
		})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.BeginAttendeeAttempt(ctx, KnowledgeInput{Kind: AttendeeKind, Platform: "fixture", Locator: attendeeFixtureOrigin, AcquisitionSignature: "synthetic", At: at},
		AttendeeRequest{Root: attendeeFixtureRoot, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("synthetic-private-file"))
	if err := s.CommitAttendeeCapture(ctx, a, AttendeeCapture{Evidence: e, InputDigest: hex.EncodeToString(sum[:]), CalendarReadID: cal.OutlookReadID, AcquiredAt: at, AdmittedAt: at}); err != nil {
		t.Fatal(err)
	}
	return a
}

func attendeeEventFixture(t *testing.T, s *Store, history bool) AttendeeSnapshot {
	t.Helper()
	got, err := s.AttendeeEventEvidence(context.Background(), nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 100, History: history})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAttendeeSnapshotHistory(t *testing.T) {
	s := newStore(t)
	a := attendeeModel(t, "A", 3)
	first := admitAttendeeFixture(t, s, a, base)
	initial := attendeeEventFixture(t, s, false)
	if len(initial.Rows) != 1 || initial.Rows[0].CaptureID != first.ID {
		t.Fatal("first physical admission missing")
	}
	admitAttendeeFixture(t, s, attendeeModel(t, "B", 4), base)
	admitAttendeeFixture(t, s, a, base.Add(-time.Hour))
	current := attendeeEventFixture(t, s, false)
	if len(current.Rows) != 1 || current.Rows[0].ID != initial.Rows[0].ID || current.Rows[0].SnapshotID != initial.Rows[0].SnapshotID ||
		current.Rows[0].CaptureID != first.ID || !current.Rows[0].AcquiredAt.Equal(base) || current.Rows[0].Retained {
		t.Fatal("A-B-A rewrote physical identity/clock or reused old scope")
	}
	if rowCount(t, s, `select count(*) from outlook_attendee_snapshots`) != 2 || rowCount(t, s, `select count(*) from outlook_attendee_members`) != 3 {
		t.Fatal("A-B-A lost immutable membership history")
	}
	if got := attendeeEventFixture(t, s, true); len(got.Rows) != 2 {
		t.Fatal("history duplicated reused physical rows or lost B")
	}
	if rowCount(t, s, `select count(*) from knowledge_sources where qualification='hxstore-attendees-v1/mapper-v1' and complete_attempt is not null`) != 0 {
		t.Fatal("native observations claimed complete")
	}
}

func TestAttendeeSameParentBindings(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "Shared", 3)
	second := e.Selections[0]
	second.SourceID, second.PayloadPos = "BB22", 99
	e.Selections = append(e.Selections, second)
	e.Parents[0].EventLinks = 2
	one, two := outlookEvent(calendar.TriFalse, base), outlookEvent(calendar.TriFalse, base)
	two.SourceID, two.GlobalID, two.ICalUID, two.SeriesKey = "BB22", "BB22", "BB22", "BB22"
	admitAttendeeFixture(t, s, e, base, one, two)
	got := attendeeEventFixture(t, s, false)
	if len(got.Rows) != 1 || got.Total != 1 || len(got.Bindings) != 1 {
		t.Fatal("event binding multiplied a physical observation")
	}
	detail, err := s.AttendeeDetail(context.Background(), nil, got.Rows[0].ID)
	if err != nil || detail.BindingsTotal != 2 || len(detail.Bindings) != 2 {
		t.Fatal("shared-parent detail lost independent bindings")
	}
}

func TestAttendeeReadIntegrity(t *testing.T) {
	for _, fault := range []string{
		`update outlook_attendee_snapshots set result_json='{}'`,
		`update outlook_attendee_occurrences set native_key=99`,
		`delete from outlook_attendee_bindings`,
		`update outlook_attendee_members set input_digest='tampered'`,
		`update outlook_attendee_members set membership_digest='tampered'`,
		`update knowledge_attempts set content_digest='tampered' where source_id is not null`,
		`update outlook_attendee_requests set request_json='{}'`,
	} {
		t.Run(fault, func(t *testing.T) {
			s := newStore(t)
			e := attendeeModel(t, "A", 3)
			admitAttendeeFixture(t, s, e, base)
			s.qExec(t, fault)
			if _, err := s.AttendeeEventEvidence(context.Background(), nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{SourceID: 999, Limit: 1}); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatal("projection hid eligible corruption")
			}
			if _, err := s.AttendeeSources(context.Background(), 0); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatal("source health certified corrupt evidence")
			}
		})
	}
}

func TestAttendeeSnapshotLocalAmbiguity(t *testing.T) {
	s := newStore(t)
	e := attendeeModel(t, "Same", 3)
	for i := 1; i < 101; i++ {
		a := e.Attendees[0]
		a.PayloadPos = i + 15
		e.Attendees = append(e.Attendees, a)
	}
	conflict := e.Attendees[0]
	conflict.DetailKey, conflict.PayloadPos = 4, 999
	e.Attendees = append(e.Attendees, conflict)
	e.AmbiguousKeys = []uint32{11}
	e.Parents = append(e.Parents, outlookattendees.Evidence{DetailKey: 4, DetailCopies: 1})
	e.Losses = []outlookattendees.Loss{{Code: "outlook_attendees_event_link_missing", Count: 1}}
	admitAttendeeFixture(t, s, e, base)
	got := attendeeEventFixture(t, s, false)
	if len(got.Rows) != 100 || got.Total != 101 || !got.Truncated || !got.Rows[0].Ambiguous {
		t.Fatal("parent/preview projection lost complete snapshot ambiguity")
	}
	detail, err := s.AttendeeDetail(context.Background(), nil, got.Rows[0].ID)
	if err != nil || !detail.Row.Ambiguous {
		t.Fatal("detail recomputed ambiguity from its preview")
	}
	admitAttendeeFixture(t, s, attendeeModel(t, "Unambiguous", 3), base)
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 1 || got.Rows[0].Ambiguous {
		t.Fatal("another snapshot's ambiguity leaked")
	}
}

func TestAttendeeResolvedGroupScope(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "tenant/A")
	commitOutlook(t, s, time.UTC, "same")
	if err := s.SetOutlookLink(context.Background(), "outlook/Main", "tenant/A", base); err != nil {
		t.Fatal(err)
	}
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 1 {
		t.Fatal("linked archived association missing")
	}
	if err := s.SetOutlookLink(context.Background(), "outlook/Main", "", base); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookLink(context.Background(), "outlook/Main", "tenant/A", base); err != nil {
		t.Fatal(err)
	}
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 0 || len(got.Coverage.Sources) != 1 {
		t.Fatal("A->none->A regained old native scope")
	}
	if got := attendeeEventFixture(t, s, true); len(got.Rows) != 1 || !got.Rows[0].Retained {
		t.Fatal("drifted history disappeared or became current")
	}
}

func TestAttendeeSourceSelectionNotDisplay(t *testing.T) {
	s := newStore(t)
	e := outlookEvent(calendar.TriFalse, base)
	e.AttendeesJSON = `[{"name":"Legacy A","address":"a@example.invalid"},{"name":"Retained A","address":"b@example.invalid"}]`
	admitAttendeeFixture(t, s, attendeeModel(t, "Native A", 3), base, e)
	e.AttendeesJSON = `[{"name":"New B","address":"new@example.invalid"}]`
	admitAttendeeFixture(t, s, attendeeModel(t, "Native B", 4), base, e)
	got := attendeeEventFixture(t, s, false)
	if len(got.Rows) != 1 || *got.Rows[0].Native.Name != "Native B" || got.Rows[0].Native.DetailKey != 4 ||
		got.Coverage.Scope != "native_source_selected_detail_not_display_roster" {
		t.Fatal("native selection was borrowed from retained display detail")
	}
}
