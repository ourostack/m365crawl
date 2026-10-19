package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestCalendarIncompleteBootstrapEnablesExactAddressLink(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putProfile(t, s, acctA.TenantID, acctA.UserID, map[string]string{"mail": "fixture.user@example.com"})
	putIdentity(t, s, "outlook/Main", "fixture.user@example.com")
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
		calEv(acctA, "bootstrap-link", "2026-09-10T09:00:00Z", nil),
	}, t1); err != nil {
		t.Fatal(err)
	}
	result, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false)
	if err != nil || result.failure != nil || !result.ReplayPending {
		t.Fatalf("incomplete retained observation: %+v %v", result, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	// No EnsureCalendar or second observation may supply the missing Teams source.
	if got := scalar(t, s, `select count(*) from calendar_source_events where source_id='bootstrap-link' and removed_at is null`); got != "1" {
		t.Errorf("first incomplete observation projected %s retained positive events, want 1", got)
	}
	if r := autoLink(t, s); r.Linked != 1 || r.Unlinked != 0 {
		t.Errorf("first incomplete observation did not enable the unique exact-address link: %+v", r)
	}
	if principal, method, active, held := linkRow(t, s, "outlook/Main"); principal != "tenant-1/aaaaaaaa-0000-0000-0000-000000000001" || method != OutlookLinkAddress || !active || !held {
		t.Errorf("cold exact-address link: principal=%q method=%q active=%t held=%t", principal, method, active, held)
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || !pending {
		t.Errorf("positive bootstrap/link discharged pending replay: %t %v", pending, err)
	}
	if got := scalar(t, s, `select count(*) from calendar_sources where source='teams' and synced_at='' and cache_fresh_at='' and coalesce(window_start,'')='' and coalesce(window_end,'')=''`); got != "1" {
		t.Errorf("bootstrap must expose one eligible Teams source without clocks/window authority, got %s", got)
	}
	if got := scalar(t, s, `select count(*) from calendar_covered_days`); got != "0" {
		t.Errorf("positive bootstrap/link verified %s days, want none", got)
	}
}

func TestCalendarPendingBackfillCannotMintFirstCacheFreshness(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "pending-first-clock", "2026-09-10T09:00:00Z", nil))
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	result, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false)
	if err != nil || result.failure != nil || !result.ReplayPending {
		t.Fatalf("qualification: incomplete source did not retain replay: %+v %v", result, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `select count(*) from calendar_sources where synced_at<>'' or cache_fresh_at<>''`); got != "0" {
		t.Errorf("incomplete observation minted %s first freshness clocks", got)
	}
	beforeRaw := scalar(t, s, `select json_array(rowid,value_json,content_hash,first_seen_at,updated_at,removed_at) from records where store='calendar'`)
	backfill, err := s.EnsureCalendar(ctx, time.UTC, t2)
	if err != nil || backfill == nil || backfill.failure != nil {
		t.Fatalf("pending archive-only backfill: %+v %v", backfill, err)
	}
	for query, want := range map[string]string{
		`select count(*) from calendar_source_events where source_id='pending-first-clock' and removed_at is null`: "1",
		`select count(*) from calendar_sources where source='teams'`:                                               "1",
		`select synced_at from calendar_sources where source='teams'`:                                              "",
		`select cache_fresh_at from calendar_sources where source='teams'`:                                         "",
		`select coalesce(window_start,'') from calendar_sources where source='teams'`:                              "",
		`select coalesce(window_end,'') from calendar_sources where source='teams'`:                                "",
		`select count(*) from calendar_covered_days`:                                                               "0",
	} {
		if got := scalar(t, s, query); got != want {
			t.Errorf("pending archive-only backfill: %s = %q, want %q", query, got, want)
		}
	}
	if after := scalar(t, s, `select json_array(rowid,value_json,content_hash,first_seen_at,updated_at,removed_at) from records where store='calendar'`); after != beforeRaw {
		t.Error("pending archive-only backfill changed the retained raw record")
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || !pending {
		t.Errorf("archive-only backfill discharged pending replay: %t %v", pending, err)
	}
}
