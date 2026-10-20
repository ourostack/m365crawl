package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestCalendarReplayAfterDepartedAccount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putRecords(t, s, t0,
		calEv(acctA, "remaining", "2026-09-10T09:00:00Z", nil),
		calEv(acctB, "departed", "2026-09-10T10:00:00Z", nil))
	if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	const peer = "tenant-2/bbbbbbbb-0000-0000-0000-000000000002"
	clocks := func() string {
		return scalar(t, s, `select json_array(window_start,window_end,synced_at,cache_fresh_at,
			(select group_concat(json_array(day,first_verified_at,last_verified_at)) from calendar_covered_days where account_id=?))
			from calendar_sources where source='teams' and account_id=?`, peer, peer)
	}
	beforeClocks := clocks()
	beforeFirst := scalar(t, s, `select first_seen_at from calendar_source_events where source_id='departed'`)
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
		calEv(acctA, "remaining", "2026-09-10T09:00:00Z", map[string]any{"subject": "Remaining changed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}),
		calEv(acctB, "departed", "2026-09-10T10:00:00Z", map[string]any{"subject": "Departed retained positive", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}),
	}, t1); err != nil {
		t.Fatal(err)
	}
	if res, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false); err != nil || res.failure != nil || !res.ReplayPending {
		t.Fatalf("incomplete observation: %+v %v", res, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	x, err = s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	present := []string{calDB("calendar", acctA)}
	if n, err := x.MarkDatabasesRemoved(calSource, present, nil, t2); err != nil || n != 1 {
		t.Fatalf("complete census must positively retire the departed database: %d %v", n, err)
	}
	res, err := x.DeriveCalendarObservation(ctx, calSource, t2, time.UTC, present, true, false)
	if err != nil || res.failure != nil || res.ReplayPending {
		t.Fatalf("departed account stranded complete source replay: %+v %v", res, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"remaining": "Remaining changed", "departed": "Departed retained positive"} {
		if got := scalar(t, s, `select subject from calendar_source_events where source_id=?`, id); got != want {
			t.Errorf("retained replay %s: %q, want %q", id, got, want)
		}
	}
	if after := clocks(); after != beforeClocks {
		t.Error("retired account gained clock/window/day authority")
	}
	if after := scalar(t, s, `select first_seen_at from calendar_source_events where source_id='departed'`); after != beforeFirst {
		t.Error("retired account lost first-seen identity")
	}
	if got := scalar(t, s, `select count(*) from calendar_source_events where source_id='departed' and removed_at is null`); got != "1" {
		t.Error("a departed database was treated as calendar-event removal evidence")
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || pending {
		t.Fatalf("retired source group left a permanent obligation: %t %v", pending, err)
	}
}

func TestCalendarIncompleteBootstrapWithExistingCalendar(t *testing.T) {
	for _, existing := range []string{"outlook", "other-teams-account"} {
		t.Run(existing, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			if existing == "outlook" {
				applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
			} else {
				putRecords(t, s, t0, calEv(acctB, "qualified-peer", "2026-09-11T09:00:00Z", nil))
			}

			if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			before := scalar(t, s, `select group_concat(json_array(source,account_id,window_start,window_end,synced_at,cache_fresh_at))
				from (select * from calendar_sources order by source,account_id)`)
			putProfile(t, s, acctA.TenantID, acctA.UserID, map[string]string{"mail": "fixture.user@example.com"})
			putIdentity(t, s, "outlook/Main", "fixture.user@example.com")
			x, err := s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Rollback()
			if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
				calEv(acctA, "new-incomplete-account", "2026-09-10T09:00:00Z", nil),
			}, t1); err != nil {
				t.Fatal(err)
			}
			res, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false)
			if err != nil || res.failure != nil || !res.ReplayPending {
				t.Fatalf("incomplete observation: %+v %v", res, err)
			}
			if err := x.Commit(); err != nil {
				t.Fatal(err)
			}
			if got := scalar(t, s, `select count(*) from calendar_source_events where source_id='new-incomplete-account' and removed_at is null`); got != "1" {
				t.Errorf("existing unrelated calendar hid the new account's retained positive: %s", got)
			}
			const account = "tenant-1/aaaaaaaa-0000-0000-0000-000000000001"
			if got := scalar(t, s, `select count(*) from calendar_sources where source='teams' and account_id=? and synced_at='' and cache_fresh_at=''
				and coalesce(window_start,'')='' and coalesce(window_end,'')=''`, account); got != "1" {
				t.Errorf("new account needs one zero-authority source row, got %s", got)
			}
			if got := scalar(t, s, `select count(*) from calendar_covered_days where source='teams' and account_id=?`, account); got != "0" {
				t.Error("incomplete new account gained verified days")
			}
			if after := scalar(t, s, `select group_concat(json_array(source,account_id,window_start,window_end,synced_at,cache_fresh_at))
				from (select * from calendar_sources where not(source='teams' and account_id=?) order by source,account_id)`, account); after != before {
				t.Error("bootstrap changed another source/account's clocks or coverage")
			}
			if result := autoLink(t, s); result.Linked != 1 {
				t.Errorf("new retained account was not eligible for exact-address linking: %+v", result)
			}
		})
	}
}

func TestCalendarIncompleteBootstrapFilteredScope(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-account", true: "selected-account"}[metadata], func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			applyOutlookBatch(t, s, OutlookStamp(3, time.UTC), outlookEvent(calendar.TriFalse, base))
			if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			putRecords(t, s, t0, calEv(acctB, "unselected-retained", "2026-09-11T09:00:00Z", nil))
			x, err := s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Rollback()
			if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
				calEv(acctA, "selected-retained", "2026-09-10T09:00:00Z", nil),
			}, t1); err != nil {
				t.Fatal(err)
			}
			var present []string
			if metadata {
				present = []string{calDB("calendar", acctA)}
			}
			result, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, present, false, true)
			if err != nil || result.failure != nil || !result.ReplayPending {
				t.Fatalf("filtered incomplete bootstrap: %+v %v", result, err)
			}
			if err := x.Commit(); err != nil {
				t.Fatal(err)
			}
			want := "0"
			if metadata {
				want = "1"
			}
			if got := scalar(t, s, `select count(*) from calendar_source_events where source_id='selected-retained'`); got != want {
				t.Errorf("selected account projection %s, want %s", got, want)
			}
			if got := scalar(t, s, `select count(*) from calendar_source_events where source_id='unselected-retained'`); got != "0" {
				t.Error("partial bootstrap crossed the selected account boundary")
			}
			if got := scalar(t, s, `select count(*) from calendar_sources where source='teams' and (synced_at<>'' or cache_fresh_at<>'' or coalesce(window_start,'')<>'' or coalesce(window_end,'')<>'')`); got != "0" {
				t.Error("partial account metadata was treated as verification authority")
			}
			if got := scalar(t, s, `select count(*) from calendar_covered_days where source='teams'`); got != "0" {
				t.Error("partial filtered bootstrap verified days")
			}
		})
	}
}
