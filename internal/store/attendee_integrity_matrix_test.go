package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeMemberIntegrityMatrix(t *testing.T) {
	for _, statement := range []string{
		`update outlook_calendar_reads set read_at='invalid'`,
		`update knowledge_attempts set started_at='invalid'`,
		`update outlook_attendee_members set owner_link_json='{}'`,
		`update outlook_attendee_members set census_json='{}'`,
		`update outlook_attendee_members set acquired_at='invalid'`,
		`update outlook_attendee_members set admitted_at='invalid'`,
		`update outlook_attendee_members set source_observed_at='invalid'`,
		`update outlook_attendee_snapshots set acquired_at='2030-01-01T00:00:00Z'`,
		`update outlook_attendee_members set link_revision=999`,
		`update outlook_attendee_occurrences set ordinal=1`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			a := admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			s.qExec(t, `pragma foreign_keys=off`)
			s.qExec(t, statement)
			if _, err := readAttendeeMember(context.Background(), s.db, a.ID); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatalf("member contradiction accepted: %v", err)
			}
		})
	}
}

func TestAttendeeBodyIndexIntegrityMatrix(t *testing.T) {
	for _, statement := range []string{
		`update outlook_attendee_snapshots set acquired_at='invalid'`,
		`update outlook_attendee_snapshots set source_observed_at='invalid'`,
		`update outlook_attendee_occurrences set native_key=99`,
		`update outlook_attendee_bindings set native_event_id='BB22'`,
		`delete from outlook_attendee_occurrences`,
		`delete from outlook_attendee_bindings`,
		`insert into outlook_attendee_occurrences(source_id,snapshot_id,ordinal,native_key,detail_key) values(1,1,1,11,3)`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			s.qExec(t, statement)
			ctx := context.Background()
			err := s.inTx(ctx, func(tx *sql.Tx) error { _, _, _, err := auditAttendeeBody(ctx, tx, 1, nil, nil, 0); return err })
			if !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatalf("body/index contradiction accepted: %v", err)
			}
		})
	}
}

func TestAttendeeScopeIntegrityMatrix(t *testing.T) {
	for _, field := range []string{"read", "owner", "acquired", "admitted", "observed", "missing_current"} {
		t.Run(field, func(t *testing.T) {
			s := newStore(t)
			a := admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			m, err := readAttendeeMember(context.Background(), s.db, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "read":
				m.ReadID = 999
			case "owner":
				m.Owner = "{"
			case "acquired":
				m.Acquired = "invalid"
			case "admitted":
				m.Admitted = "invalid"
			case "observed":
				m.Observed = "invalid"
			case "missing_current":
				s.qExec(t, `delete from outlook_calendar_read_state`)
			}
			if _, err := attendeeScope(context.Background(), s.db, m); err == nil {
				t.Fatal("invalid scope accepted")
			}
		})
	}
}

func TestAttendeeAdmissionAssociationAndReuseRefusal(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := attendeeModel(t, "A", 3)
	admitAttendeeFixture(t, s, a, base)
	admitAttendeeFixture(t, s, attendeeModel(t, "B", 4), base)
	request := nativeAttempt(t, s)
	c := AttendeeCapture{Evidence: a, InputDigest: strings.Repeat("a", 64), CalendarReadID: 1, AcquiredAt: base, AdmittedAt: base}
	if err := s.CommitAttendeeCapture(ctx, request, c); err == nil || err.Error() != "outlook_attendee_calendar_unassociated" {
		t.Fatal("stale read associated", err)
	}
	c.CalendarReadID = 2
	s.qExec(t, `update outlook_attendee_snapshots set result_json='{}' where rowid=1`)
	if err := s.CommitAttendeeCapture(ctx, request, c); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("A-B-A repaired corrupt A", err)
	}
}

func TestAttendeeCoherentLinkBarrier(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	addTeamsAccount(t, s, "tenant/A")
	addTeamsAccount(t, s, "tenant/B")
	commitOutlook(t, s, time.UTC, "same")
	if err := s.SetOutlookLink(ctx, "outlook/Main", "tenant/A", base); err != nil {
		t.Fatal(err)
	}
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = s.attendeeView(ctx, func(q knowledgeReader, _ bool) error {
		groups, _, err := resolveCalendarGroups(ctx, q, "", calendar.Key(outlookEvent(calendar.TriFalse, base)))
		if err != nil {
			return err
		}
		if err := writer.SetOutlookLink(ctx, "outlook/Main", "tenant/B", base); err != nil {
			return err
		}
		got := emptyAttendeeSnapshot()
		if err := attendeeEventInView(ctx, q, nil, groups[0], AttendeeFilter{Limit: 100}, &got); err != nil {
			return err
		}
		if len(got.Rows) != 1 || got.Coverage.Sources[0].CurrentLink.Principal != "tenant/A" {
			t.Fatal("event/link frontier crossed snapshots")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 0 || got.Coverage.Sources[0].LinkState != "drifted" {
		t.Fatal("later transaction hid link mutation")
	}
}

func TestAttendeeCoherentAdmissionBarrier(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = s.attendeeView(ctx, func(q knowledgeReader, _ bool) error {
		groups, _, err := resolveCalendarGroups(ctx, q, "", calendar.Key(outlookEvent(calendar.TriFalse, base)))
		if err != nil {
			return err
		}
		admitAttendeeFixture(t, writer, attendeeModel(t, "B", 4), base)
		got := emptyAttendeeSnapshot()
		if err := attendeeEventInView(ctx, q, nil, groups[0], AttendeeFilter{Limit: 100}, &got); err != nil {
			return err
		}
		if len(got.Rows) != 1 || *got.Rows[0].Native.Name != "A" || *got.Coverage.Sources[0].CurrentReadID != 1 {
			t.Fatal("event/native frontiers crossed read snapshots")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 1 || *got.Rows[0].Native.Name != "B" {
		t.Fatal("next transaction hid completed admission")
	}
}
