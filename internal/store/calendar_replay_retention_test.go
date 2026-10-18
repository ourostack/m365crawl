package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCalendarReplayFailedProjectionCannotLoseObligation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "retention", "2026-09-10T09:00:00Z", nil))
	if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	putRecords(t, s, t1, calEv(acctA, "retention", "2026-09-10T09:00:00Z",
		map[string]any{"subject": "Retained changed raw", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}))
	for _, q := range []string{
		`create trigger projection_refusal before update on calendar_source_events begin select raise(abort,'owned projection refused'); end`,
		`create trigger retention_refusal before insert on meta when new.key like 'calendar_replay/%' begin select raise(abort,'owned replay retention refused'); end`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || pending {
		t.Fatalf("complete-read discriminator must start without a pending mark: %t %v", pending, err)
	}
	before := dump(t, s)
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	_, err = x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, []string{calDB("calendar", acctA)}, true, false)
	x.Rollback()
	if err == nil || !strings.Contains(err.Error(), "owned replay retention refused") {
		t.Fatalf("post-projection obligation retention did not fail closed: %v", err)
	}
	if after := dump(t, s); after != before {
		t.Fatalf("failed retention changed raw/rowids/projection/authority/FTS/meta:\nbefore %s\nafter %s", before, after)
	}
	if _, err := s.db.Exec(`drop trigger retention_refusal`); err != nil {
		t.Fatal(err)
	}
	x, err = s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	result, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, []string{calDB("calendar", acctA)}, true, false)
	if err != nil || result.failure == nil || !strings.Contains(result.failure.Error(), "owned projection refused") || !result.ReplayPending {
		t.Fatalf("qualified projection failure must retain a durable replay obligation: %+v %v", result, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || !pending {
		t.Fatalf("committed failure lost replay: %t %v", pending, err)
	}
	if got := scalar(t, s, `select subject from calendar_source_events where source_id='retention'`); got != "Subject retention" {
		t.Fatalf("failed projection replaced qualified content: %q", got)
	}
	if _, err := s.db.Exec(`drop trigger projection_refusal`); err != nil {
		t.Fatal(err)
	}
	x, err = s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	result, err = x.DeriveCalendarObservation(ctx, calSource, t1.Add(time.Minute), time.UTC, []string{calDB("calendar", acctA)}, true, false)
	if err != nil || result.failure != nil || result.ReplayPending {
		t.Fatalf("complete recovery did not discharge replay: %+v %v", result, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `select subject from calendar_source_events where source_id='retention'`); got != "Retained changed raw" {
		t.Fatalf("complete replay failed to project retained positive content: %q", got)
	}
}
