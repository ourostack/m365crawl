package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type attendeeLengthReader struct {
	knowledgeReader
	length int64
	bodies int
}

func (q *attendeeLengthReader) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "select length(") {
		return q.knowledgeReader.QueryRowContext(ctx, `select ?`, q.length)
	}
	q.bodies++
	return q.knowledgeReader.QueryRowContext(ctx, query, args...)
}

func TestAttendeeLiteralSQLCaps(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	ctx := context.Background()
	for _, c := range []struct {
		table, column, key string
		limit              int
	}{
		{"outlook_attendee_snapshots", "result_json", "rowid", 402653184},
		{"outlook_attendee_members", "census_json", "attempt_id", 4194304},
	} {
		for _, n := range []int64{-1, int64(c.limit), int64(c.limit) + 1} {
			q := &attendeeLengthReader{knowledgeReader: s.db, length: n}
			if _, err := attendeeBlob(ctx, q, c.table, c.column, c.key, 1, c.limit); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatal(err)
			}
			want := 0
			if n == int64(c.limit) {
				want = 1
			}
			if q.bodies != want {
				t.Fatalf("literal cap %d, length %d fetched %d bodies", c.limit, n, q.bodies)
			}
		}
	}
}

func TestAttendeeForeignSourceAndScopeBudget(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	ctx := context.Background()
	a := nativeAttempt(t, s)
	read, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Other", Origin: attendeeFixtureOrigin, At: base, Zone: time.UTC}, outlookRun)
	if err != nil {
		t.Fatal(err)
	}
	c := AttendeeCapture{Evidence: attendeeModel(t, "B", 4), InputDigest: strings.Repeat("a", 64), CalendarReadID: read.OutlookReadID, AcquiredAt: base, AdmittedAt: base}
	if err := s.CommitAttendeeCapture(ctx, a, c); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("source account replacement accepted", err)
	}
	oversized := "tenant/" + strings.Repeat("A", 262145)
	addTeamsAccount(t, s, oversized)
	if err := s.SetOutlookLink(ctx, "outlook/Main", oversized, base); err != nil {
		t.Fatal(err)
	}
	c.CalendarReadID = 1
	if err := s.CommitAttendeeCapture(ctx, a, c); !errors.Is(err, outlookmeetingattendees.ErrTooLarge) {
		t.Fatal("oversized captured scope accepted", err)
	}
}

func TestAttendeeUnknownLinkAndAccountFilter(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "tenant/A")
	commitOutlook(t, s, time.UTC, "same")
	ctx := context.Background()
	if err := s.SetOutlookLink(ctx, "outlook/Main", "tenant/A", base); err != nil {
		t.Fatal(err)
	}
	s.qExec(t, `update calendar_account_links set principal_id='' where source='outlook'`)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	if got := attendeeEventFixture(t, s, false); len(got.Rows) != 0 || got.Coverage.Sources[0].LinkState != "unknown" {
		t.Fatal("unknown owner became current")
	}
	if _, err := s.AttendeeDetail(ctx, &teamsdesktop.Account{TenantID: "tenant", UserID: "A"}, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("account filter accepted unknown owner", err)
	}
}

func TestAttendeeRejectEmptyPositiveSource(t *testing.T) {
	s := newStore(t)
	commitOutlook(t, s, time.UTC, "same")
	a := nativeAttempt(t, s)
	s.qExec(t, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,'unresolved',?)`, a.LocatorID, AttendeeQualification)
	s.qExec(t, `insert into outlook_attendee_sources(source_id,calendar_account,origin) values(1,'outlook/Main',?)`, attendeeFixtureOrigin)
	if _, err := s.AttendeeSources(context.Background(), 0); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
		t.Fatal("empty positive native source certified", err)
	}
}
