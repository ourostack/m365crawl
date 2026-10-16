package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeSchemaNamespace(t *testing.T) {
	ctx := context.Background()
	for _, fault := range []string{"table", "column", "trigger"} {
		t.Run(fault, func(t *testing.T) {
			path := writableArchivePath(t)
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "table":
				s.qExec(t, `drop table outlook_attendee_members`)
			case "column":
				s.qExec(t, `alter table outlook_attendee_members drop column input_digest`)
			case "trigger":
				s.qExec(t, `drop trigger outlook_attendee_link_insert`)
			}
			if _, err := s.OutlookReadReceipt(ctx, "outlook/NoHits"); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Errorf("partial current namespace read as healthy: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(ctx, path); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("current writer repaired partial namespace: %v", err)
			}
		})
	}
}

func TestAttendeeIdentityFaultSweeps(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) { commitOutlook(t, s, time.UTC, "same") },
		func(ctx context.Context, s *Store) error {
			_, err := s.OutlookReadReceipt(ctx, "outlook/Main")
			return err
		})
	sweepReadFaults(t, func(t *testing.T, s *Store) { commitOutlook(t, s, time.UTC, "same") },
		func(ctx context.Context, s *Store) error {
			_, err := s.CheckAttendeeOrigin(ctx, "outlook/Main", "/synthetic/Main/HxStore.hxd")
			return err
		})
	sweepReadFaults(t, func(*testing.T, *Store) {},
		func(ctx context.Context, s *Store) error {
			_, err := s.CheckAttendeeOrigin(ctx, "outlook/New", "/synthetic/New/HxStore.hxd")
			return err
		})
	sweepCalendar(t, func(*testing.T, *Store) {}, func(ctx context.Context, s *Store) error {
		_, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/New", At: base, Zone: time.UTC, Origin: "/synthetic/New/HxStore.hxd"}, outlookRun)
		return err
	})
}

func TestAttendeeSchemaUpgradeAndSecondOpen(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	commitOutlook(t, s, time.UTC, "same", outlookEvent(calendar.TriFalse, base))
	for name := range attendeeTriggerSQL {
		s.qExec(t, `drop trigger `+name)
	}
	s.qExec(t, `pragma foreign_keys=off`)
	for _, name := range []string{"outlook_attendee_members", "outlook_attendee_bindings", "outlook_attendee_occurrences", "outlook_attendee_snapshots", "outlook_attendee_requests", "outlook_attendee_sources", "outlook_calendar_read_state", "outlook_calendar_reads", "outlook_link_state", "outlook_link_events"} {
		s.qExec(t, `drop table `+name)
	}
	s.qExec(t, `update schema_migrations set version=18`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ro.OutlookReadReceipt(ctx, "outlook/Main")
	if err != nil || receipt.ID != 0 {
		t.Fatal("old optional namespace fabricated a read")
	}
	if got, err := ro.AttendeeSources(ctx, 0); err != nil || len(got.Coverage.Sources) != 0 {
		t.Fatal("old optional namespace invented sources", err)
	}
	if _, err := ro.AttendeeDetail(ctx, nil, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("old optional namespace invented detail", err)
	}
	if got, err := ro.AttendeeEventEvidence(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base)), AttendeeFilter{Limit: 1}); err != nil || len(got.Rows) != 0 {
		t.Fatal("old optional namespace invented event evidence", err)
	}
	if got, err := ro.CalendarEvent(ctx, nil, calendar.Key(outlookEvent(calendar.TriFalse, base))); err != nil || len(got.NativeAttendeeEvidence.Coverage.Sources) != 0 {
		t.Fatal("old archive lost legacy event or invented native summary", err)
	}
	if _, err := ro.AttendeeCaptureReused(ctx, KnowledgeAttempt{ID: 1, LocatorID: 1}); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("old archive invented reuse receipt", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
	if err != nil || revision != 0 {
		t.Fatal("migration lost baseline frontier")
	}
	if state, err := s.CheckAttendeeOrigin(ctx, "outlook/Main", "/synthetic/Main/HxStore.hxd"); err != nil || state != "unknown" {
		t.Fatal("migration inferred an origin")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAttendeeReceiptAndScopeCorruption(t *testing.T) {
	for _, sql := range []string{
		`update outlook_calendar_reads set receipt_digest='invalid'`,
		`update outlook_calendar_reads set read_at='invalid'`,
		`update outlook_calendar_read_state set account_id='outlook/Foreign'`,
		`delete from outlook_calendar_read_state`,
		`update outlook_calendar_read_state set read_id=(select min(rowid) from outlook_calendar_reads)`,
	} {
		t.Run(sql, func(t *testing.T) {
			s := newStore(t)
			commitOutlook(t, s, time.UTC, "same")
			commitOutlook(t, s, time.UTC, "same")
			s.qExec(t, `pragma foreign_keys=off`)
			s.qExec(t, sql)
			if _, err := s.OutlookReadReceipt(context.Background(), "outlook/Main"); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatal("receipt inconsistency admitted")
			}
		})
	}
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := auditAttendeeNamespace(ctx, s.db); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled audit lost context")
	}
	cause := errors.New("synthetic-private-diagnostic")
	err := attendeeIntegrityError(context.Background(), cause)
	if err.Error() != "outlook_attendee_archive_corrupt" || !errors.Is(err, cause) || !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("integrity diagnostic leaked or lost cause")
	}
}

func TestAttendeeLinkHelperAndWriterFaults(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		commitOutlook(t, s, time.UTC, "same")
	}
	sweepReadFaults(t, seed, func(ctx context.Context, s *Store) error {
		_, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
		return err
	})
	sweepCalendar(t, seed, func(ctx context.Context, s *Store) error { _, err := s.AutoLinkOutlook(ctx, base); return err })
	s := newStore(t)
	if _, err := attendeeLinkRevision(context.Background(), s.db, "outlook/Unknown"); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("missing frontier fabricated baseline")
	}
	commitOutlook(t, s, time.UTC, "same")
	s.qExec(t, `delete from outlook_link_state where account_id='outlook/Main'`)
	if err := s.inTx(context.Background(), func(tx *sql.Tx) error { return ensureAttendeeAccount(context.Background(), tx, "outlook/Main") }); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("bootstrap repaired missing known-account state")
	}
}

func TestAttendeeLinkMovesAndRollback(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	commitOutlook(t, s, time.UTC, "same")
	s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at) values('outlook','outlook/Main','tenant/A','config','2031-03-01T00:00:00Z')`)
	first, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
	if err != nil {
		t.Fatal(err)
	}
	s.qExec(t, `update calendar_account_links set account_id='outlook/Other'`)
	old, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
	if err != nil || old <= first {
		t.Fatal("account move did not advance old Outlook scope")
	}
	moved, err := attendeeLinkRevision(ctx, s.db, "outlook/Other")
	if err != nil || moved <= first {
		t.Fatal("account move did not initialize/advance new Outlook scope")
	}
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `update calendar_account_links set principal_id='tenant/B'`); err != nil {
			return err
		}
		return errors.New("synthetic rollback")
	}); err == nil {
		t.Fatal("rollback fixture succeeded")
	}
	after, err := attendeeLinkRevision(ctx, s.db, "outlook/Other")
	if err != nil || after != moved {
		t.Fatal("rolled-back mutation changed current scope")
	}
	s.qExec(t, `update calendar_account_links set source='teams'`)
	ended, err := attendeeLinkRevision(ctx, s.db, "outlook/Other")
	if err != nil || ended <= moved {
		t.Fatal("source move lost old Outlook mutation")
	}
	s.qExec(t, `update calendar_account_links set source='outlook'`)
	returned, err := attendeeLinkRevision(ctx, s.db, "outlook/Other")
	if err != nil || returned <= ended {
		t.Fatal("source return regained old scope")
	}
	sweepCalendar(t, func(t *testing.T, s *Store) { commitOutlook(t, s, time.UTC, "same") },
		func(ctx context.Context, s *Store) error { return s.SetOutlookLink(ctx, "outlook/Main", "", base) })
}
