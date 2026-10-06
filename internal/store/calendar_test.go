package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamscal"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const calSource = "profile|origin"

func calDB(manager string, a teamsdesktop.Account) string {
	return "Teams:" + manager + ":react-web-client:" + a.TenantID + ":" + a.UserID + ":" + a.Locale
}

// calEvent is the JSON of one calendar record, in the shape the mapper reads. extra overrides or
// adds fields; a nil value removes one.
func calEvent(id, start string, extra map[string]any) []byte {
	st, _ := time.Parse(time.RFC3339, start)
	m := map[string]any{
		"objectId": id, "iCalUID": "uid-" + id, "cleanGlobalObjectId": "uid-" + id, "eventType": "Single",
		"subject": "Subject " + id, "eventTimeZone": "Utc", "isAllDayEvent": false, "isCancelled": false,
		"startTime":        map[string]any{"$date": start},
		"endTime":          map[string]any{"$date": st.Add(time.Hour).Format(time.RFC3339)},
		"lastModifiedTime": map[string]any{"$date": "2026-08-01T00:00:00Z"},
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func calRec(a teamsdesktop.Account, manager, store, key string, value []byte) teamsdesktop.GenericRecord {
	acct := a
	k, _ := json.Marshal(key)
	return teamsdesktop.GenericRecord{Account: &acct, Database: calDB(manager, a), Store: store, KeyJSON: k, ValueJSON: value}
}

func calEv(a teamsdesktop.Account, id, start string, extra map[string]any) teamsdesktop.GenericRecord {
	return calRec(a, "calendar", "calendar", id, calEvent(id, start, extra))
}

// putRecords archives records at the given time and returns nothing; it commits.
func putRecords(t *testing.T, s *Store, at time.Time, rs ...teamsdesktop.GenericRecord) {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	if _, err := x.UpsertRecords(calSource, rs, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
}

// removeRecord marks every record of the key as no longer in the cache.
func removeRecord(t *testing.T, s *Store, at time.Time, key string) {
	t.Helper()
	k, _ := json.Marshal(key)
	if _, err := s.db.Exec(`update records set removed_at=? where key_json=?`, at.UTC().Format(timeLayout), string(k)); err != nil {
		t.Fatal(err)
	}
}

func derive(t *testing.T, s *Store, at time.Time) CalendarResult {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	res, err := x.DeriveCalendar(context.Background(), calSource, at, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	return res
}

func scalar(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

var (
	t0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t1.Add(time.Hour)
)

// A sync maps the calendar and recap records, in the three calendar tables.
func TestDeriveFillsTheCalendarTables(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0,
		calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil),
		calEv(acctA, "e2", "2026-09-11T09:00:00Z", map[string]any{"iCalUID": "uid-shared"}),
		calRec(acctA, "calendar", "calendar-internal-data", "lastSuccesfulSyncTimestamp", []byte(`{"key":"lastSuccesfulSyncTimestamp","value":{"$date":"2026-09-01T09:30:00Z"}}`)),
		calRec(acctA, "meetforwork-manager", "meetforwork-meeting-catch-up", "uid-shared", []byte(`{"iCalUid":"uid-shared","meetingEndTime":{"$date":"2026-09-11T10:00:00Z"},"data":[{"callId":"call-1","url":"https://example.invalid/rec","tasks":[{"headline":"Do it","text":"Do it now","ownerDisplayName":"Alex Fixture"}]}]}`)),
		calRec(acctA, "meeting-recap-manager", "meeting-recap-catchup", "recap-1", []byte(`{"callId":"call-1","shortSummary":"Short","actionItems":[{"actionItemTitle":"T","ownerName":"Alex Fixture"}]}`)),
		calRec(acctA, "calendar-manager", "events", "decoy", []byte(`{"objectId":"decoy"}`)),
	)
	res := derive(t, s, t0)
	if c := res.Counts; c.Events.Inserted != 2 || c.Recaps.Inserted != 1 || c.Recaps.Updated != 1 || c.RecapItems.Inserted != 2 || c.Gone != 0 || len(res.Omissions) != 0 {
		t.Fatalf("counts: %+v omissions %v", c, res.Omissions)
	}
	for q, want := range map[string]string{
		`select count(*) from calendar_source_events`:                   "2",
		`select count(*) from calendar_recaps`:                          "1",
		`select count(*) from calendar_recap_items`:                     "2",
		`select count(*) from calendar_covered_days`:                    "2",
		`select window_start from calendar_sources`:                     "2026-09-10T00:00:00.000Z",
		`select window_end from calendar_sources`:                       "2026-09-12T00:00:00.000Z",
		`select cache_fresh_at from calendar_sources`:                   "2026-09-01T09:30:00.000Z",
		`select ical_uid from calendar_recaps`:                          "uid-shared",
		`select count(*) from calendar_source_events where ical_uid=''`: "0",
	} {
		if got := scalar(t, s, q); got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	// The generic records stay as they were: the calendar is a projection of them.
	if n := scalar(t, s, `select count(*) from records`); n != "6" {
		t.Errorf("records = %s", n)
	}
}

func TestDeriveOnlyMapsRowsUpdatedInThisSync(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "e2", "2026-09-10T11:00:00Z", nil))
	derive(t, s, t0)
	// Sync 2 changes one record and rewrites nothing else (an unchanged record is not rewritten).
	putRecords(t, s, t1, calEv(acctA, "e2", "2026-09-10T11:00:00Z", map[string]any{"subject": "Renamed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}))
	res := derive(t, s, t1)
	if c := res.Counts.Events; c.Seen != 1 || c.Updated != 1 {
		t.Fatalf("events: %+v", c)
	}
	if got := scalar(t, s, `select subject from calendar_source_events where source_id='e2'`); got != "Renamed" {
		t.Fatalf("subject %q", got)
	}
	// A third sync reads the same records again: nothing is mapped and no event row changes.
	putRecords(t, s, t2, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "e2", "2026-09-10T11:00:00Z", map[string]any{"subject": "Renamed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}))
	before := scalar(t, s, `select group_concat(seen_at || subject, '|') from (select * from calendar_source_events order by source_id)`)
	res = derive(t, s, t2)
	if c := res.Counts; c.Events.Seen != 0 || c.Gone != 0 {
		t.Fatalf("an unchanged sync mapped %+v", c)
	}
	if after := scalar(t, s, `select group_concat(seen_at || subject, '|') from (select * from calendar_source_events order by source_id)`); after != before {
		t.Fatalf("event rows changed:\n%s\n%s", before, after)
	}
}

func TestDeriveGoneOnlyWhenDayStillLoaded(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0,
		calEv(acctA, "keep", "2026-09-10T09:00:00Z", nil),
		calEv(acctA, "gone", "2026-09-10T11:00:00Z", nil),
		calEv(acctA, "master", "2026-09-10T08:00:00Z", map[string]any{"eventType": "RecurringMaster"}))
	derive(t, s, t0)
	removeRecord(t, s, t1, "gone")
	removeRecord(t, s, t1, "master")
	res := derive(t, s, t1)
	if res.Counts.Gone != 1 {
		t.Fatalf("gone = %d", res.Counts.Gone)
	}
	if got := scalar(t, s, `select group_concat(source_id) from calendar_source_events where removed_at is not null`); got != "gone" {
		t.Fatalf("removed events: %q", got)
	}
	// The row keeps everything it had.
	if got := scalar(t, s, `select subject from calendar_source_events where source_id='gone'`); got != "Subject gone" {
		t.Fatalf("subject %q", got)
	}
}

func TestDeriveEvictedDayKeepsEventsLive(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "d1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "d2", "2026-09-11T09:00:00Z", nil), calEv(acctA, "d2b", "2026-09-11T10:00:00Z", nil))
	derive(t, s, t0)
	// The cache dropped the whole of the 11th: eviction, not deletion.
	removeRecord(t, s, t1, "d2")
	removeRecord(t, s, t1, "d2b")
	res := derive(t, s, t1)
	if res.Counts.Gone != 0 || scalar(t, s, `select count(*) from calendar_source_events where removed_at is not null`) != "0" {
		t.Fatalf("an evicted day's events were marked gone: %+v", res.Counts)
	}
}

func TestCoveredDaysCumulative(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "d1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "d2", "2026-09-14T09:00:00Z", nil))
	derive(t, s, t0)
	removeRecord(t, s, t1, "d2")
	derive(t, s, t1)
	if got := scalar(t, s, `select group_concat(day) from (select day from calendar_covered_days order by day)`); got != "2026-09-10,2026-09-14" {
		t.Fatalf("covered days shrank: %q", got)
	}
	if got := scalar(t, s, `select window_end from calendar_sources`); got != "2026-09-15T00:00:00.000Z" {
		t.Fatalf("window shrank to %q", got)
	}
	if got := scalar(t, s, `select first_verified_at from calendar_covered_days where day='2026-09-10'`); got != "2026-09-01T10:00:00.000Z" {
		t.Fatalf("first verified %q", got)
	}
	if got := scalar(t, s, `select last_verified_at from calendar_covered_days where day='2026-09-10'`); got != "2026-09-01T11:00:00.000Z" {
		t.Fatalf("last verified %q", got)
	}
	// A day seen later widens the window.
	putRecords(t, s, t2, calEv(acctA, "d0", "2026-09-02T09:00:00Z", nil))
	derive(t, s, t2)
	if got := scalar(t, s, `select window_start from calendar_sources`); got != "2026-09-02T00:00:00.000Z" {
		t.Fatalf("window start %q", got)
	}
}

func TestDeriveReappearingRecordClearsGone(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "keep", "2026-09-10T09:00:00Z", nil), calEv(acctA, "back", "2026-09-10T11:00:00Z", nil))
	derive(t, s, t0)
	removeRecord(t, s, t1, "back")
	derive(t, s, t1)
	if scalar(t, s, `select count(*) from calendar_source_events where removed_at is not null`) != "1" {
		t.Fatal("not gone")
	}
	// The record is back in the cache: the sync rewrites it (clearing its removed_at).
	putRecords(t, s, t2, calEv(acctA, "back", "2026-09-10T11:00:00Z", nil))
	res := derive(t, s, t2)
	if res.Counts.Events.Seen != 1 || scalar(t, s, `select count(*) from calendar_source_events where removed_at is not null`) != "0" {
		t.Fatalf("still gone: %+v", res.Counts)
	}
	// A later removal of a day that is loaded again is gone again, and a record removed in an
	// earlier sync is found gone once its day is loaded.
	putRecords(t, s, t2.Add(time.Hour), calEv(acctA, "late", "2026-09-20T09:00:00Z", nil))
	derive(t, s, t2.Add(time.Hour))
	removeRecord(t, s, t2.Add(2*time.Hour), "late")
	derive(t, s, t2.Add(2*time.Hour))
	if scalar(t, s, `select count(*) from calendar_source_events where source_id='late' and removed_at is not null`) != "0" {
		t.Fatal("a lone event of an evicted day was marked gone")
	}
	putRecords(t, s, t2.Add(3*time.Hour), calEv(acctA, "other", "2026-09-20T12:00:00Z", nil))
	res = derive(t, s, t2.Add(3*time.Hour))
	if res.Counts.Gone != 1 {
		t.Fatalf("the day came back without the event: %+v", res.Counts)
	}
}

// The window and the verified days come from the live records by SQL, not from what the sync read.
func TestDeriveSkipsMastersAndUnreadableStartsWhenFindingDays(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0,
		calEv(acctA, "m", "2026-09-10T09:00:00Z", map[string]any{"eventType": "RecurringMaster"}),
		calEv(acctA, "bad", "2026-09-10T09:00:00Z", map[string]any{"startTime": "soon"}),
		calEv(acctA, "num", "2026-09-12T09:00:00Z", map[string]any{"startTime": 1788858000000}))
	derive(t, s, t0)
	if got := scalar(t, s, `select group_concat(day) from calendar_covered_days`); got != "2026-09-08" {
		t.Fatalf("covered days: %q", got)
	}
}

func TestDeriveCountsOmissionsByCode(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0,
		calEv(acctA, "zone", "2026-09-10T09:00:00Z", map[string]any{"eventTimeZone": "FixtureUnknownSt"}),
		calEv(acctA, "allday", "2026-09-10T09:30:00Z", map[string]any{"isAllDayEvent": true}),
		calRec(acctA, "calendar", "calendar", "broken", []byte(`{"objectId":"broken"}`)),
		calRec(acctA, "calendar", "calendar", "garbage", []byte(`[1,2]`)),
		calRec(acctA, "meetforwork-manager", "meetforwork-meeting-catch-up", "uid-x", []byte(`{"iCalUid":"uid-x","data":[{"url":"no call id"},{"callId":"c","tasks":["not an object"]}]}`)),
		calRec(acctA, "meetforwork-manager", "meetforwork-meeting-catch-up", "uid-y", []byte(`"scalar"`)),
		calRec(acctA, "meeting-recap-manager", "meeting-recap-catchup", "r1", []byte(`{"shortSummary":"no call id"}`)),
		calRec(acctA, "meeting-recap-manager", "meeting-recap-catchup", "r2", []byte(`{"callId":"c2","shortSummary":"ok"}`)),
	)
	res := derive(t, s, t0)
	want := map[string]int{OmitCalendarUnknownZone: 1, OmitCalendarAllDayUnaligned: 1, OmitCalendarUnmapped: 6}
	if fmt.Sprint(res.Omissions) != fmt.Sprint(want) {
		t.Fatalf("omissions %v, want %v", res.Omissions, want)
	}
	if res.Counts.Events.Inserted != 2 || res.Counts.Refused != 0 {
		t.Fatalf("counts %+v", res.Counts)
	}
	// The unknown zone is stored and shown, not dropped.
	if got := scalar(t, s, `select time_zone || '/' || time_zone_iana from calendar_source_events where source_id='zone'`); got != "FixtureUnknownSt/" {
		t.Fatalf("zone %q", got)
	}
}

// Rows of a database whose name carries no account, or of a manager that is not Teams' calendar,
// are not the calendar's.
func TestDeriveIgnoresRowsWithoutACalendarAccount(t *testing.T) {
	s := newStore(t)
	odd := func(store, key string, value []byte) teamsdesktop.GenericRecord {
		return teamsdesktop.GenericRecord{Database: "Teams:odd", Store: store, KeyJSON: []byte(`"` + key + `"`), ValueJSON: value}
	}
	other := calRec(acctA, "other-manager", "calendar", "x", calEvent("x", "2026-09-10T09:00:00Z", nil))
	putRecords(t, s, t0, odd("calendar", "live", calEvent("live", "2026-09-10T09:00:00Z", nil)),
		odd("calendar", "gone", calEvent("gone", "2026-09-10T09:00:00Z", nil)),
		odd("calendar-internal-data", "lastSuccesfulSyncTimestamp", []byte(`{"value":{"$date":"2026-09-01T09:30:00Z"}}`)),
		other)
	removeRecord(t, s, t0, "gone")
	res := derive(t, s, t0)
	if res.Counts.Events.Seen != 0 || scalar(t, s, `select count(*) from calendar_source_events`) != "0" || scalar(t, s, `select count(*) from calendar_sources`) != "0" {
		t.Fatalf("counts %+v", res.Counts)
	}
}

// Every source and account is a group of its own, in a fixed order.
func TestEnsureCalendarGroupsBySourceAndAccount(t *testing.T) {
	s := newStore(t)
	sameTenant := teamsdesktop.Account{TenantID: acctA.TenantID, UserID: "aaaaaaaa-0000-0000-0000-000000000009", Locale: "en-us"}
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil), calEv(acctB, "e2", "2026-09-10T09:00:00Z", nil), calEv(sameTenant, "e4", "2026-09-10T09:00:00Z", nil))
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.UpsertRecords("other|origin", []teamsdesktop.GenericRecord{calEv(acctA, "e3", "2026-09-11T09:00:00Z", nil)}, t0); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := s.EnsureCalendar(context.Background(), time.UTC, t1)
	if err != nil || res.Counts.Events.Inserted != 4 {
		t.Fatalf("%+v %v", res, err)
	}
	if scalar(t, s, `select count(*) from calendar_sources`) != "3" {
		t.Fatal("one calendar_sources row per account expected")
	}
	if got := scalar(t, s, `select window_start || '/' || window_end from calendar_sources where account_id like 'tenant-1/%'`); got != "2026-09-10T00:00:00.000Z/2026-09-12T00:00:00.000Z" {
		t.Fatalf("the account's window spans both sources: %q", got)
	}
}

func TestDeriveFreshnessFallbacks(t *testing.T) {
	s := newStore(t)
	other := acctB
	// acctA has a calendar database and no timestamp: the newest event modification time.
	// acctB has only a recap database: no calendar, so no freshness at all.
	putRecords(t, s, t0,
		calEv(acctA, "e1", "2026-09-10T09:00:00Z", map[string]any{"lastModifiedTime": map[string]any{"$date": "2026-08-05T00:00:00Z"}}),
		calRec(other, "meeting-recap-manager", "meeting-recap-catchup", "r2", []byte(`{"callId":"c2","shortSummary":"ok"}`)))
	derive(t, s, t0)
	if got := scalar(t, s, `select cache_fresh_at from calendar_sources where account_id=?`, "tenant-1/"+acctA.UserID); got != "2026-08-05T00:00:00.000Z" {
		t.Fatalf("fresh %q", got)
	}
	if got := scalar(t, s, `select cache_fresh_at from calendar_sources where account_id=?`, "tenant-2/"+acctB.UserID); got != "" {
		t.Fatalf("an account with no calendar database is fresh at %q", got)
	}
	// Later copies with no newer modification keep the stored newest time.
	putRecords(t, s, t1, calEv(acctA, "e2", "2026-09-10T10:00:00Z", map[string]any{"lastModifiedTime": map[string]any{"$date": "2026-07-01T00:00:00Z"}}))
	derive(t, s, t1)
	if got := scalar(t, s, `select cache_fresh_at from calendar_sources where account_id=?`, "tenant-1/"+acctA.UserID); got != "2026-08-05T00:00:00.000Z" {
		t.Fatalf("fresh moved back to %q", got)
	}
	// With no event time at all, an account that has a calendar database is fresh as of the sync.
	s2 := newStore(t)
	putRecords(t, s2, t0, calRec(acctA, "calendar", "calendar-internal-data", "syncState", []byte(`{"key":"syncState"}`)))
	derive(t, s2, t0)
	if got := scalar(t, s2, `select cache_fresh_at from calendar_sources`); got != "2026-09-01T10:00:00.000Z" {
		t.Fatalf("fresh %q", got)
	}
	// An unreadable stamp is not used.
	s3 := newStore(t)
	putRecords(t, s3, t0, calRec(acctA, "calendar", "calendar-internal-data", "lastSuccesfulSyncTimestamp", []byte(`{"value":"never"}`)))
	derive(t, s3, t0)
	if got := scalar(t, s3, `select cache_fresh_at from calendar_sources`); got != "2026-09-01T10:00:00.000Z" {
		t.Fatalf("fresh %q", got)
	}
}

func TestRecordKeyOfNonStringKey(t *testing.T) {
	if recordKey(`"abc"`) != "abc" || recordKey(`42`) != `42` || recordKey(`["a",1]`) != `["a",1]` {
		t.Fatal("recordKey")
	}
}

func TestCalendarCountsAdd(t *testing.T) {
	var c CalendarCounts
	c.Add(CalendarCounts{Events: Counts{Seen: 1, Inserted: 1}, Recaps: Counts{Seen: 2, Updated: 2}, RecapItems: Counts{Unchanged: 3}, Gone: 4, Linked: 5, Refused: 6})
	c.Add(CalendarCounts{Events: Counts{Seen: 1, Inserted: 1}, Gone: 1})
	if c.Events.Seen != 2 || c.Events.Inserted != 2 || c.Recaps.Updated != 2 || c.RecapItems.Unchanged != 3 || c.Gone != 5 || c.Linked != 5 || c.Refused != 6 {
		t.Fatalf("%+v", c)
	}
}

// An archive from before the calendar fills its calendar from the records it holds, with no cache.
func TestBackfillFromRecordsNeedsNoCache(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "e2", "2026-09-10T11:00:00Z", nil),
		calRec(acctA, "meeting-recap-manager", "meeting-recap-catchup", "r1", []byte(`{"callId":"c1","shortSummary":"S","actionItems":[{"actionItemTitle":"T"}]}`)))
	// A record that was evicted from the cache keeps its event, live: its day is not loaded now.
	putRecords(t, s, t0, calEv(acctA, "old", "2026-08-01T09:00:00Z", nil))
	removeRecord(t, s, t0, "old")
	res, err := s.EnsureCalendar(context.Background(), time.UTC, t1)
	if err != nil || res == nil {
		t.Fatalf("EnsureCalendar = %v, %v", res, err)
	}
	if c := res.Counts; c.Events.Inserted != 3 || c.Recaps.Inserted != 1 || c.RecapItems.Inserted != 1 || c.Gone != 0 {
		t.Fatalf("counts %+v", c)
	}
	if scalar(t, s, `select count(*) from calendar_source_events where removed_at is null`) != "3" {
		t.Fatal("the evicted event is not live")
	}
	if got := scalar(t, s, `select value from meta where key='calendar_derivation'`); got != calendarDerivation(teamscal.MapperVersion, teamsdesktop.RulesStamp()) {
		t.Fatalf("meta %q", got)
	}
	again, err := s.EnsureCalendar(context.Background(), time.UTC, t2)
	if err != nil || again != nil {
		t.Fatalf("a second call did work: %v %v", again, err)
	}
	// A removed record on a day the cache still holds is gone in the backfill too.
	s2 := newStore(t)
	putRecords(t, s2, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil), calEv(acctA, "e2", "2026-09-10T11:00:00Z", nil))
	removeRecord(t, s2, t0, "e2")
	res, err = s2.EnsureCalendar(context.Background(), time.UTC, t1)
	if err != nil || res.Counts.Gone != 1 {
		t.Fatalf("backfill gone: %+v %v", res, err)
	}
}

func TestBackfillRunsOnMapperVersionChange(t *testing.T) {
	s := newStore(t)
	rich := map[string]any{"bodyContent": "<p>Agenda</p>", "bodyContentType": "html", "attendees": []any{map[string]any{"name": "Alex Fixture", "address": "alex@example.invalid", "type": "Required"}}}
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", rich))
	if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	// Teams later served a thin copy; the record now lacks the detail, the calendar keeps it.
	putRecords(t, s, t1, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil))
	derive(t, s, t1)
	if scalar(t, s, `select count(*) from calendar_source_events where attendees_json<>''`) != "1" {
		t.Fatal("a thin copy erased the attendees")
	}
	// An older mapper had derived the calendar: every record is mapped again and captured.
	cur := calendarDerivation(teamscal.MapperVersion, teamsdesktop.RulesStamp())
	old := strings.Replace(cur, fmt.Sprintf("mapper=%d", teamscal.MapperVersion), "mapper=0", 1)
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, old); err != nil {
		t.Fatal(err)
	}
	res, err := s.EnsureCalendar(context.Background(), time.UTC, t2)
	if err != nil || res == nil || res.Counts.Events.Seen != 1 {
		t.Fatalf("EnsureCalendar = %+v, %v", res, err)
	}
	if scalar(t, s, `select count(*) from calendar_source_events where attendees_json<>''`) != "1" {
		t.Fatal("a mapper change lost the detail the calendar had captured")
	}
	if got := scalar(t, s, `select value from meta where key='calendar_derivation'`); got != cur {
		t.Fatalf("meta %q", got)
	}
}

const marker = "SECRETMARKER"

func markerRecords(a teamsdesktop.Account) []teamsdesktop.GenericRecord {
	return []teamsdesktop.GenericRecord{
		calEv(a, "e1", "2026-09-10T09:00:00Z", map[string]any{
			"subject": "Planning " + marker, "bodyContent": "<p>" + marker + "</p>", "bodyContentType": "html",
			"attendees": []any{map[string]any{"name": marker, "address": "x@example.invalid"}},
		}),
		calRec(a, "meetforwork-manager", "meetforwork-meeting-catch-up", "uid-e1", []byte(`{"iCalUid":"uid-e1","data":[{"callId":"c1","headline":"`+marker+`","tasks":[{"headline":"`+marker+`","text":"`+marker+`"}]}]}`)),
		calRec(a, "meeting-recap-manager", "meeting-recap-catchup", "r1", []byte(`{"callId":"c1","shortSummary":"`+marker+` summary"}`)),
	}
}

func markerCount(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	for _, q := range []string{
		`select count(*) from calendar_source_events where subject like '%` + marker + `%' or body_html like '%` + marker + `%' or body_text like '%` + marker + `%' or attendees_json like '%` + marker + `%' or detail_raw_json like '%` + marker + `%'`,
		`select count(*) from calendar_recaps where headline like '%` + marker + `%' or short_summary like '%` + marker + `%'`,
		`select count(*) from calendar_recap_items where superseded_at is null and (title like '%` + marker + `%' or text like '%` + marker + `%')`,
	} {
		var c int
		if err := s.db.QueryRow(q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		n += c
	}
	return n
}

func TestRulesChangeRescrubsDerivedCalendar(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, markerRecords(acctA)...)
	if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	if n := markerCount(t, s); n != 3 {
		t.Fatalf("the marker is in %d derived places before the rules change, want 3 (event, recap row, item)", n)
	}
	// A new rule redacts the marker, and the stored stamp is the one of the older rules.
	calendarScrub = func(b []byte) ([]byte, int) {
		return []byte(strings.ReplaceAll(string(b), marker, "[redacted]")), 1
	}
	t.Cleanup(func() { calendarScrub = teamsdesktop.Scrub })
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, "0.old")); err != nil {
		t.Fatal(err)
	}
	res, err := s.EnsureCalendar(context.Background(), time.UTC, t1)
	if err != nil || res == nil {
		t.Fatalf("EnsureCalendar = %v, %v", res, err)
	}
	if n := markerCount(t, s); n != 0 {
		t.Fatalf("the marker survives in %d derived places", n)
	}
	// What the records still back was rebuilt, and the blanked items are hidden, not deleted.
	if scalar(t, s, `select count(*) from calendar_source_events where removed_at is null and body_html like '%[redacted]%'`) != "1" {
		t.Fatal("the event was not rebuilt")
	}
	if scalar(t, s, `select count(*) from calendar_recap_items where superseded_at is not null`) != "1" || scalar(t, s, `select count(*) from calendar_recap_items where superseded_at is null`) != "1" {
		t.Fatal("items: the old one is not superseded or the new one is missing")
	}
}

func TestDeniedStoreClearsDerivedCalendar(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, markerRecords(acctA)...)
	if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	calendarDenied = func(name string) bool { return name == "calendar" }
	t.Cleanup(func() { calendarDenied = teamsdesktop.Denied })
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, "0.old")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureCalendar(context.Background(), time.UTC, t1); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `select count(*) from calendar_source_events where removed_at is not null and detail_raw_json='' and body_html='' and attendees_json='' and subject=''`); got != "1" {
		t.Fatalf("the denied store's event keeps content or is live: %s", got)
	}
	if n := markerCount(t, s) - 2; n != 0 { // the recap rows are not from the denied store
		t.Fatalf("marker count after: %d", n)
	}
}

// An archive at the previous schema version gains the calendar tables, with the nullable flag
// columns and the link table, on its next writable open.
func TestArchiveUpgradeAddsCalendarTables(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"calendar_source_events", "calendar_sources", "calendar_covered_days", "calendar_account_links", "calendar_matches", "calendar_recaps", "calendar_recap_items"} {
		if _, err := s.db.Exec(`drop table ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`update schema_migrations set version = 4`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open v4 archive: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := rowCount(t, s, `select max(version) from schema_migrations`); v != 5 {
		t.Fatalf("version %d", v)
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where name in ('calendar_source_events','calendar_sources','calendar_covered_days','calendar_account_links','calendar_matches','calendar_recaps','calendar_recap_items')`); n != 7 {
		t.Fatalf("calendar tables: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('calendar_source_events') where name in ('all_day','is_organizer','is_private','cancelled','is_online_meeting','has_attachments') and "notnull"=0`); n != 6 {
		t.Fatalf("nullable flag columns: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where name in ('calendar_account_links_principal','calendar_account_links_one_per_source')`); n != 2 {
		t.Fatalf("link table indexes: %d", n)
	}
	// The next sync fills them from the records the archive holds.
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil))
	if res, err := s.EnsureCalendar(ctx, time.UTC, t1); err != nil || res == nil || res.Counts.Events.Inserted != 1 {
		t.Fatalf("EnsureCalendar = %+v, %v", res, err)
	}
}

func TestCalendarCacheState(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c, err := s.CalendarCache(ctx)
	if err != nil || c.Accounts != 0 || !c.HasTables || !c.FreshAt.IsZero() {
		t.Fatalf("empty: %+v %v", c, err)
	}
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.ApplyAccount(ctx, acctB))
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", map[string]any{"lastModifiedTime": map[string]any{"$date": "2026-08-05T00:00:00Z"}}))
	derive(t, s, t0)
	c, err = s.CalendarCache(ctx)
	if err != nil || c.Accounts != 2 || c.WithoutDatabase != 1 || c.FreshAt.Format(time.RFC3339) != "2026-08-05T00:00:00Z" {
		t.Fatalf("state: %+v %v", c, err)
	}
	// An archive from before the calendar tables has none.
	if _, err := s.db.Exec(`drop table calendar_sources`); err != nil {
		t.Fatal(err)
	}
	if c, err = s.CalendarCache(ctx); err != nil || c.HasTables {
		t.Fatalf("old archive: %+v %v", c, err)
	}
}

func TestCalendarFailuresRollBack(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		putRecords(t, s, t0, markerRecords(acctA)...)
		putRecords(t, s, t0, calEv(acctB, "b1", "2026-09-10T09:00:00Z", nil)) // an account with no cache timestamp
		putRecords(t, s, t0, calRec(acctA, "calendar", "calendar-internal-data", "lastSuccesfulSyncTimestamp", []byte(`{"value":{"$date":"2026-09-01T09:30:00Z"}}`)))
		// A second event on the same day, removed from the cache: gone, which is a second apply.
		putRecords(t, s, t0, calEv(acctA, "e2", "2026-09-10T10:00:00Z", nil))
		removeRecord(t, s, t0, "e2")
		must0(s.ApplyAccount(context.Background(), acctA))
	}
	t.Run("derive", func(t *testing.T) {
		sweepCalendar(t, seed, func(ctx context.Context, s *Store) error {
			x, err := s.Begin(ctx)
			if err != nil {
				return err
			}
			defer x.Rollback()
			if _, err := x.DeriveCalendar(ctx, calSource, t0, time.UTC); err != nil {
				return err
			}
			return x.Commit()
		})
	})
	t.Run("ensure", func(t *testing.T) {
		sweepCalendar(t, seed, func(ctx context.Context, s *Store) error {
			_, err := s.EnsureCalendar(ctx, time.UTC, t1)
			return err
		})
	})
	t.Run("ensure/replace", func(t *testing.T) {
		sweepCalendar(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, "0.old")); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			_, err := s.EnsureCalendar(ctx, time.UTC, t1)
			return err
		})
	})
	t.Run("cache", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seed(t, s)
			derive(t, s, t0)
		}, func(ctx context.Context, s *Store) error {
			_, err := s.CalendarCache(ctx)
			return err
		})
	})
}

// sweepCalendar is sweepFaults without the rowsclose kind: the core (internal/calendar) ignores a
// failure to close a result set it has read to the end, which cannot lose data.
func sweepCalendar(t *testing.T, setup func(t *testing.T, s *Store), op func(ctx context.Context, s *Store) error, nullSafe ...int) {
	t.Helper()
	for _, kind := range faultKinds {
		if kind == "rowsclose" {
			continue
		}
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			sweepKind(t, s, injectFaults(t, s), kind, true, op, nullSafe)
		})
	}
}
