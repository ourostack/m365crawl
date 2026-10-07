package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func sourcesOf(t *testing.T, s *Store, f CalendarSourcesFilter) map[string]CalendarSource {
	t.Helper()
	res, err := s.CalendarSources(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.NoTables {
		t.Fatal("no calendar tables")
	}
	out := map[string]CalendarSource{}
	for _, r := range res.Rows {
		out[r.AccountID] = r
	}
	return out
}

// A Teams row counts the live rows by what they hold, the covered days with their newest
// verification, the recaps that have text (never a row with none) and the action items the reader
// would see, and names the zones no IANA id was found for.
func TestCalendarSourcesCountsATeamsAccount(t *testing.T) {
	s := qArchive(t)
	s.qExec(t, `update calendar_source_events set time_zone='QFixtureSt', time_zone_iana='' where source_id in ('o2','o3')`)
	s.qExec(t, `update calendar_source_events set removed_at='2026-11-05T00:00:00.000Z' where source_id='o3'`)
	s.qExec(t, `insert into calendar_covered_days(source,account_id,day,first_verified_at,last_verified_at) values
	  ('teams',?,'2026-11-03','2026-11-01T10:00:00.000Z','2026-11-01T10:00:00.000Z'),('teams',?,'2026-11-10','2026-11-01T10:00:00.000Z','2026-11-02T10:00:00.000Z')`, qTeams, qTeams)
	// A recap with no text and no event is a row that does not count as content; one with an event does count as linked.
	s.qExec(t, `insert into calendar_recaps(account_id,call_id,first_seen_at,updated_at) values(?,'call-2','2026-11-04T00:00:00.000Z','2026-11-04T00:00:00.000Z')`, qTeams)

	rows := sourcesOf(t, s, CalendarSourcesFilter{Now: qt("2026-11-06T00:00:00Z")})
	r, ok := rows[qTeams]
	if !ok || len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
	if r.Source != "teams" || r.Principal != qTeams || r.Link != "none" || r.Outlook != nil {
		t.Fatalf("identity %+v", r)
	}
	if r.CoveredDays != 2 || !r.LastVerifiedAt.Equal(qt("2026-11-02T10:00:00Z")) {
		t.Fatalf("covered days %d, last verified %v", r.CoveredDays, r.LastVerifiedAt)
	}
	if r.EventsLive != 3 || r.EventsRemoved != 1 || r.WithDetail != 1 || r.WithAttendees != 1 || r.WithBody != 1 || r.Online != 3 {
		t.Fatalf("event counts %+v", r)
	}
	if r.RecapsTotal != 2 || r.RecapsContent != 1 || r.RecapsLinked != 1 || r.RecapActions != 1 {
		t.Fatalf("recap counts content %d linked %d actions %d", r.RecapsContent, r.RecapsLinked, r.RecapActions)
	}
	if len(r.UnknownZones) != 1 || r.UnknownZones[0] != "QFixtureSt" {
		t.Fatalf("unknown zones %v", r.UnknownZones)
	}
	if r.WindowStart.IsZero() || r.WindowEnd.IsZero() || r.SyncedAt.IsZero() || r.CacheFreshAt.IsZero() {
		t.Fatalf("window and freshness %+v", r)
	}

	// --account keeps the account and the accounts linked to it, and nothing else.
	acct := acctA
	acct.TenantID, acct.UserID = "tenant-1", "aaaaaaaa-0000-0000-0000-000000000001"
	if got := sourcesOf(t, s, CalendarSourcesFilter{Account: &acct}); len(got) != 1 {
		t.Fatalf("filtered rows %v", got)
	}
	other := acctB
	if got := sourcesOf(t, s, CalendarSourcesFilter{Account: &other}); len(got) != 0 {
		t.Fatalf("another account's rows %v", got)
	}
}

func TestCalendarSourcesCapsTheZoneNames(t *testing.T) {
	s := qArchive(t)
	for i := 0; i < maxUnknownZones+5; i++ {
		s.qExec(t, `insert into calendar_source_events(source,account_id,event_key,composite_key,source_id,time_zone,first_seen_at,seen_at) values('teams',?,?,?,?,?,'x','x')`,
			qTeams, "z"+string(rune('A'+i)), "z"+string(rune('A'+i)), "z"+string(rune('A'+i)), "Zone"+string(rune('A'+i)))
	}
	if got := sourcesOf(t, s, CalendarSourcesFilter{})[qTeams].UnknownZones; len(got) != maxUnknownZones {
		t.Fatalf("%d zone names", len(got))
	}
}

func TestCalendarSourcesWithoutTables(t *testing.T) {
	s := qArchive(t)
	s.qExec(t, `drop table calendar_recaps`)
	res, err := s.CalendarSources(context.Background(), CalendarSourcesFilter{})
	if err != nil || !res.NoTables || len(res.Rows) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// An Outlook row shows its status as the archive remembers it, the next time a read is allowed, the
// last read's census and the link; a profile whose first read failed still has a row.
func TestCalendarSourcesOutlookRows(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	at := qt("2026-11-01T10:00:00Z")
	now := at.Add(time.Minute)
	interval := 5 * time.Minute
	read := OutlookRead{BlocksFound: 200, BlocksInvalid: 10, UnknownLayouts: []OutlookLayout{{Class: 0x6b, Tag: 0x456, Count: 3}}, UnmappedValues: map[string]int{"response": 2}}
	if _, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriFalse, at)}, FreshAt: at, At: at, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC), Read: read}, outlookRun); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookLastAttempt(ctx, "Main", at); err != nil {
		t.Fatal(err)
	}
	f := CalendarSourcesFilter{Now: now, ReadInterval: interval}

	o := sourcesOf(t, s, f)["outlook/Main"]
	if o.Outlook == nil || o.Source != "outlook" || o.Principal != "outlook/Main" || o.Link != "none" {
		t.Fatalf("row %+v", o)
	}
	x := o.Outlook
	if x.Status != SourceOK || !x.NextReadAfter.Equal(at.Add(interval)) || !x.LastAttemptAt.Equal(at) || x.IntervalSecond != 300 || x.Failure != nil {
		t.Fatalf("outlook %+v", x)
	}
	if x.BlocksRatio == nil || *x.BlocksRatio != 0.05 || !x.CensusAsOf.Equal(at) || len(x.UnknownLayouts) != 1 || x.UnknownLayouts[0] != (OutlookLayout{0x6b, 0x456, 3}) || x.UnmappedValues["response"] != 2 {
		t.Fatalf("census %+v", x)
	}
	if o.EventsLive != 1 || o.CoveredDays == 0 {
		t.Fatalf("counts %+v", o)
	}

	// Skipped by the interval: the status says so while the next read is still ahead, and stops saying
	// so when the interval has passed.
	if err := s.SetOutlookSkipped(ctx, "Main"); err != nil {
		t.Fatal(err)
	}
	if got := sourcesOf(t, s, f)["outlook/Main"].Outlook; got.Status != SourceSkippedInterval {
		t.Fatalf("status %q", got.Status)
	}
	f.Now = at.Add(time.Hour)
	if got := sourcesOf(t, s, f)["outlook/Main"].Outlook; got.Status != SourceOK || !got.NextReadAfter.IsZero() {
		t.Fatalf("status %q next %v", got.Status, got.NextReadAfter)
	}
	// Looking at the store forgets the skip and says when it happened.
	if err := s.SetOutlookChecked(ctx, "Main", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.Now = now
	if got := sourcesOf(t, s, f)["outlook/Main"].Outlook; got.Status != SourceOK || !got.LastCheckedAt.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("%+v", got)
	}
	// A failure keeps the census of the last good read, and says how old it is.
	if err := s.SetOutlookFailure(ctx, "outlook/Main", &OutlookFailure{Code: "outlook_layout_unsupported", Message: "m", Fix: "f"}); err != nil {
		t.Fatal(err)
	}
	if got := sourcesOf(t, s, f)["outlook/Main"].Outlook; got.Status != SourceUnsupportedLayout || got.Failure == nil || len(got.UnknownLayouts) != 1 || got.BlocksRatio == nil || !got.CensusAsOf.Equal(at) {
		t.Fatalf("a failure row keeps the last good census: %+v", got)
	}
	if err := s.SetOutlookFailure(ctx, "outlook/Main", nil); err != nil {
		t.Fatal(err)
	}

	// A failure names the status by the guard code, and a profile that never read has a row anyway.
	for code, want := range map[string]string{"outlook_store_version": SourceUnsupportedVersion, "outlook_layout_unsupported": SourceUnsupportedLayout, "no_full_disk_access": SourceUnreadable} {
		if err := s.SetOutlookFailure(ctx, "outlook/Two", &OutlookFailure{Code: code, Message: "m", Fix: "f", Exit: 3}); err != nil {
			t.Fatal(err)
		}
		two := sourcesOf(t, s, f)["outlook/Two"]
		if two.Outlook == nil || two.Outlook.Status != want || two.Outlook.Failure.Code != code || two.Outlook.Failure.Fix != "f" || two.EventsLive != 0 || !two.Outlook.LastReadAt.IsZero() || two.Outlook.BlocksRatio != nil || !two.Outlook.CensusAsOf.IsZero() {
			t.Fatalf("%s: %+v", code, two)
		}
	}
	s.qExec(t, `insert into meta(key, value) values('outlook_failure:outlook/Bad', 'not json')`)
	if bad := sourcesOf(t, s, f)["outlook/Bad"].Outlook; bad.Status != SourceUnreadable || bad.Failure.Code != "unreadable" {
		t.Fatalf("%+v", bad)
	}

	// A link joins the profile to the Teams account: its own row says so and names the principal, and
	// the Teams row keeps its own counts.
	teamsBefore := sourcesOf(t, s, f)[qTeams]
	s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at) values('outlook','outlook/Main',?,'config','2026-11-01T10:00:00.000Z')`, qTeams)
	rows := sourcesOf(t, s, f)
	if m := rows["outlook/Main"]; m.Link != "config" || m.Principal != qTeams {
		t.Fatalf("linked row %+v", m)
	}
	if rows[qTeams].EventsLive != teamsBefore.EventsLive || rows[qTeams].Link != "none" {
		t.Fatalf("the Teams row changed: %+v vs %+v", rows[qTeams], teamsBefore)
	}
	acct := acctA
	if got := sourcesOf(t, s, CalendarSourcesFilter{Account: &acct, Now: now}); got["outlook/Main"].AccountID == "" || got["outlook/Two"].AccountID != "" {
		t.Fatalf("a filter on the Teams account must keep its linked profile only: %v", got)
	}
	// The order is Teams first, then by account.
	res, _ := s.CalendarSources(ctx, f)
	var order []string
	for _, r := range res.Rows {
		order = append(order, r.AccountID)
	}
	if strings.Join(order, ",") != qTeams+",outlook/Bad,outlook/Main,outlook/Two" {
		t.Fatalf("order %v", order)
	}
}

func TestOutlookReadIsKeptWithTheCommit(t *testing.T) {
	s := qArchive(t)
	at := qt("2026-11-01T10:00:00Z")
	want := OutlookRead{BlocksFound: 4, BlocksInvalid: 1}
	if _, err := s.CommitOutlook(context.Background(), OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriFalse, at)}, FreshAt: at, At: at, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC), Read: want}, outlookRun); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`select value from meta where key='outlook_read:outlook/Main'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got OutlookRead
	if err := json.Unmarshal([]byte(raw), &got); err != nil || got.BlocksFound != 4 || got.BlocksInvalid != 1 {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestCalendarSourcesReadFailuresSurface(t *testing.T) {
	sweepReadFaults(t, func(t *testing.T, s *Store) {
		qSeed(t, s)
		at := qt("2026-11-01T10:00:00Z")
		s.qExec(t, `insert into calendar_covered_days(source,account_id,day,first_verified_at,last_verified_at) values('teams',?,'2026-11-03','2026-11-01T10:00:00.000Z','2026-11-01T10:00:00.000Z')`, qTeams)
		s.qExec(t, `update calendar_source_events set time_zone='QSt', time_zone_iana='' where source_id='o2'`)
		if _, err := s.CommitOutlook(context.Background(), OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriFalse, at)}, FreshAt: at, At: at, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC)}, outlookRun); err != nil {
			t.Fatal(err)
		}
		s.qLink(t)
	}, func(ctx context.Context, s *Store) error {
		_, err := s.CalendarSources(ctx, CalendarSourcesFilter{ReadInterval: time.Minute})
		return err
	})
}
