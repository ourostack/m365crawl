package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
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
	present := presentDatabases(t, s)
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	res, err := x.DeriveCalendar(context.Background(), calSource, at, time.UTC, present)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	return res
}

// presentDatabases is every database the archive holds records of, as a sync that read them all
// would report it.
func presentDatabases(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`select distinct database from records where source=? and removed_at is null`, calSource)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
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
	if got := scalar(t, s, `select value from meta where key='calendar_derivation'`); got != calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), teamsdesktop.RulesStamp()) {
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
	cur := calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), teamsdesktop.RulesStamp())
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
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), "0.old")); err != nil {
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
	if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), "0.old")); err != nil {
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

// failedLoss is the cause of a rebuild that was rolled back and counted, which is what the fault
// sweep looks for.
func failedLoss(res *CalendarResult, err error) error {
	if err == nil && res != nil {
		return res.failure
	}
	return err
}

func TestCalendarFailuresRollBack(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		putRecords(t, s, t0, markerRecords(acctA)...)
		putRecords(t, s, t0, calRec(acctA, "calendar", "calendar", "broken", []byte(`{"objectId":"broken"}`))) // a standing loss
		putRecords(t, s, t0, calEv(acctB, "b1", "2026-09-10T09:00:00Z", nil))                                  // an account with no cache timestamp
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
			res, err := x.DeriveCalendar(ctx, calSource, t0, time.UTC, nil)
			if err == nil {
				err = res.failure // isolated and counted; the sweep wants the cause
			}
			if err != nil {
				return err
			}
			return x.Commit()
		})
	})
	t.Run("ensure", func(t *testing.T) {
		sweepCalendar(t, seed, func(ctx context.Context, s *Store) error {
			return failedLoss(s.EnsureCalendar(ctx, time.UTC, t1))
		})
	})
	t.Run("ensure/replace", func(t *testing.T) {
		sweepCalendar(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`update meta set value=? where key='calendar_derivation'`, calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), "0.old")); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			return failedLoss(s.EnsureCalendar(ctx, time.UTC, t1))
		})
	})
	t.Run("ensure/lost", func(t *testing.T) {
		sweepCalendar(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{`delete from calendar_recap_items`, `delete from calendar_recaps`, `delete from calendar_covered_days`, `delete from calendar_sources`, `delete from calendar_source_events`} {
				if _, err := s.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
		}, func(ctx context.Context, s *Store) error {
			return failedLoss(s.EnsureCalendar(ctx, time.UTC, t1))
		})
	})
	t.Run("ensure/zone", func(t *testing.T) {
		// A zone change forgets the covered days first; that delete can fail too.
		sweepCalendar(t, func(t *testing.T, s *Store) {
			seed(t, s)
			if _, err := s.EnsureCalendar(context.Background(), time.UTC, t0); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			return failedLoss(s.EnsureCalendar(ctx, time.FixedZone("JST", 9*3600), t1))
		})
	})
	t.Run("losses", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seed(t, s)
			derive(t, s, t0)
		}, func(ctx context.Context, s *Store) error {
			_, err := s.CalendarLosses(ctx, calSource, nil)
			return err
		}, 1)
	})
	t.Run("isolated", func(t *testing.T) {
		// The savepoint's own statements failing: each is reported, not counted.
		s := newStore(t)
		inj := injectFaults(t, s)
		for at := 1; at <= 3; at++ {
			x, err := s.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			inj.arm("exec", at)
			_, err = isolated(context.Background(), x.tx, func() (CalendarResult, error) { return CalendarResult{}, errors.New("derive failed") })
			fired := inj.disarm()
			x.Rollback()
			if !fired || !errors.Is(err, errInjected) {
				t.Fatalf("exec #%d: fired %v err %v", at, fired, err)
			}
		}
		// And a success whose release fails.
		x, err := s.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		inj.arm("exec", 2)
		_, err = isolated(context.Background(), x.tx, func() (CalendarResult, error) { return CalendarResult{}, nil })
		inj.disarm()
		x.Rollback()
		if !errors.Is(err, errInjected) {
			t.Fatalf("release: %v", err)
		}
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

const calSource2 = "profile2|origin"

// putFrom archives records under another source (a second Teams profile).
func putFrom(t *testing.T, s *Store, source string, at time.Time, rs ...teamsdesktop.GenericRecord) {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	if _, err := x.UpsertRecords(source, rs, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
}

func deriveFrom(t *testing.T, s *Store, source string, at time.Time, present ...string) CalendarResult {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	res, err := x.DeriveCalendar(context.Background(), source, at, time.UTC, present)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	return res
}

func removeFrom(t *testing.T, s *Store, source, key string, at time.Time) {
	t.Helper()
	k, _ := json.Marshal(key)
	if _, err := s.db.Exec(`update records set removed_at=? where source=? and key_json=?`, at.UTC().Format(timeLayout), source, string(k)); err != nil {
		t.Fatal(err)
	}
}

// Two profiles hold one account. An event is gone only when no profile still holds it on a day the
// cache loaded, and the answer is the same whether the profiles are derived one at a time (a sync)
// or together (a backfill), in either order.
func TestGoneNeedsNoProfileToHoldTheEventLive(t *testing.T) {
	for _, order := range []string{"p1 first", "p2 first", "backfill"} {
		t.Run(order, func(t *testing.T) {
			s := newStore(t)
			day := "2026-09-10T09:00:00Z"
			// Both profiles hold e1 and a second event of the day, so the day is loaded in both.
			for _, src := range []string{calSource, calSource2} {
				putFrom(t, s, src, t0, calEv(acctA, "e1", day, nil), calEv(acctA, "keep", "2026-09-10T11:00:00Z", nil))
			}
			derive(t, s, t0)
			dbs := []string{calDB("calendar", acctA)}
			// Profile 1 drops e1 from the day; profile 2 still holds it.
			removeFrom(t, s, calSource, "e1", t1)
			switch order {
			case "p1 first":
				deriveFrom(t, s, calSource, t1, dbs...)
				deriveFrom(t, s, calSource2, t1, dbs...)
			case "p2 first":
				deriveFrom(t, s, calSource2, t1, dbs...)
				deriveFrom(t, s, calSource, t1, dbs...)
			default:
				if _, err := s.db.Exec(`delete from meta where key='calendar_derivation'`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.EnsureCalendar(context.Background(), time.UTC, t1); err != nil {
					t.Fatal(err)
				}
			}
			if got := scalar(t, s, `select removed_at from calendar_source_events where source_id='e1'`); got != "" {
				t.Fatalf("profile 2 holds e1, but it is gone since %q", got)
			}
			// Once profile 2 drops it too, nothing holds it and it is gone.
			removeFrom(t, s, calSource2, "e1", t2)
			deriveFrom(t, s, calSource2, t2, dbs...)
			if got := scalar(t, s, `select removed_at is not null from calendar_source_events where source_id='e1'`); got != "1" {
				t.Fatalf("no profile holds e1, but it is live")
			}
			if got := scalar(t, s, `select removed_at is null from calendar_source_events where source_id='keep'`); got != "1" {
				t.Fatal("an event no profile removed went away")
			}
		})
	}
}

// A sync writes freshness only for the accounts whose cache it read.
func TestDeriveMovesFreshnessOnlyForTheAccountsItRead(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "a1", "2026-09-10T09:00:00Z", nil), calEv(acctB, "b1", "2026-09-10T09:00:00Z", nil))
	deriveFrom(t, s, calSource, t0, calDB("calendar", acctA), calDB("calendar", acctB))
	a, b := acctA.TenantID+"/"+acctA.UserID, acctB.TenantID+"/"+acctB.UserID
	syncedB := scalar(t, s, `select synced_at from calendar_sources where account_id=?`, b)
	verifiedB := scalar(t, s, `select last_verified_at from calendar_covered_days where account_id=?`, b)
	if syncedB == "" || verifiedB == "" {
		t.Fatalf("the first sync wrote no freshness for b: %q %q", syncedB, verifiedB)
	}
	// A later sync reads only account A; A's rows change, B's do not.
	putRecords(t, s, t1, calEv(acctA, "a1", "2026-09-10T09:00:00Z", map[string]any{"subject": "Renamed"}))
	deriveFrom(t, s, calSource, t1, calDB("calendar", acctA))
	if got := scalar(t, s, `select synced_at from calendar_sources where account_id=?`, a); got != t1.UTC().Format(timeLayout) {
		t.Fatalf("a was read at %s but synced_at is %q", t1, got)
	}
	if got := scalar(t, s, `select last_verified_at from calendar_covered_days where account_id=?`, a); got != t1.UTC().Format(timeLayout) {
		t.Fatalf("a's days verified at %q", got)
	}
	if got := scalar(t, s, `select synced_at from calendar_sources where account_id=?`, b); got != syncedB {
		t.Fatalf("a sync that did not read b moved its synced_at: %q -> %q", syncedB, got)
	}
	if got := scalar(t, s, `select last_verified_at from calendar_covered_days where account_id=?`, b); got != verifiedB {
		t.Fatalf("a sync that did not read b moved its covered days: %q -> %q", verifiedB, got)
	}
}

// A rebuild from the records reads no cache: it says no account synced now and verifies no day it
// already had; a day it learns of is recorded.
func TestRebuildKeepsFreshnessItDidNotSee(t *testing.T) {
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "a1", "2026-09-10T09:00:00Z", nil))
	derive(t, s, t0)
	a := acctA.TenantID + "/" + acctA.UserID
	putRecords(t, s, t1, calEv(acctA, "a2", "2026-09-12T09:00:00Z", nil))
	if _, err := s.db.Exec(`delete from meta where key='calendar_derivation'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureCalendar(context.Background(), time.UTC, t2); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `select synced_at from calendar_sources where account_id=?`, a); got != t0.UTC().Format(timeLayout) {
		t.Fatalf("a rebuild moved synced_at to %q", got)
	}
	if got := scalar(t, s, `select last_verified_at from calendar_covered_days where account_id=? and day='2026-09-10'`, a); got != t0.UTC().Format(timeLayout) {
		t.Fatalf("a rebuild re-verified a known day: %q", got)
	}
	if got := scalar(t, s, `select last_verified_at from calendar_covered_days where account_id=? and day='2026-09-12'`, a); got != t2.UTC().Format(timeLayout) {
		t.Fatalf("a day the rebuild learned of: %q", got)
	}
}

// What cannot be mapped, or is refused, is counted by every sync until the record changes, with its
// reason, and never carries content.
func TestLossesAreRecountedEverySyncByReason(t *testing.T) {
	s := newStore(t)
	broken := calRec(acctA, "calendar", "calendar", "broken", []byte(`{"objectId":"broken"}`))
	notJSON := calRec(acctA, "calendar", "calendar", "garbled", []byte(`"a string, not an object"`))
	putRecords(t, s, t0, calEv(acctA, "ok", "2026-09-10T09:00:00Z", nil), broken, notJSON)
	res := derive(t, s, t0)
	if res.Omissions[OmitCalendarUnmapped] != 2 {
		t.Fatalf("first: %v", res.Omissions)
	}
	// A later sync that writes no record of the calendar counts the same two.
	putRecords(t, s, t1, calEv(acctA, "other", "2026-09-11T09:00:00Z", nil))
	again := derive(t, s, t1)
	if again.Omissions[OmitCalendarUnmapped] != 2 {
		t.Fatalf("second: %v", again.Omissions)
	}
	rows, err := s.db.Query(`select reason, sum(n) from calendar_losses where kind='unmapped' group by reason order by reason`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var reason string
		var n int
		if err := rows.Scan(&reason, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s=%d", reason, n))
	}
	if strings.Join(got, "; ") != "startTime is missing or not a time with a zone offset=1; value is not an object=1" {
		t.Fatalf("reasons: %v", got)
	}
	// CalendarLosses is the same count without deriving, for a sync that reads nothing.
	if n, err := s.CalendarLosses(context.Background(), calSource, nil); err != nil || n[OmitCalendarUnmapped] != 2 {
		t.Fatalf("%v %v", n, err)
	}
	if n, err := s.CalendarLosses(context.Background(), calSource, &acctB); err != nil || n[OmitCalendarUnmapped] != 0 {
		t.Fatalf("another account: %v %v", n, err)
	}
	// Fixing a record clears its loss; removing one stops counting it.
	putRecords(t, s, t2, calEv(acctA, "broken", "2026-09-12T09:00:00Z", nil))
	removeRecord(t, s, t2, "garbled")
	if fixed := derive(t, s, t2); fixed.Omissions[OmitCalendarUnmapped] != 0 || len(fixed.Omissions) != 0 {
		t.Fatalf("after: %v", fixed.Omissions)
	}
}

// An event the core refuses is counted every sync too.
func TestRefusedEventsAreRecountedEverySync(t *testing.T) {
	s := newStore(t)
	// The mapper validates what it maps, so a refusal by the core is reached through its seam.
	real := calendarMapEvent
	t.Cleanup(func() { calendarMapEvent = real })
	calendarMapEvent = func(a teamsdesktop.Account, key string, raw []byte, z *time.Location) (calendar.Event, teamscal.MapNotes, error) {
		e, notes, err := real(a, key, raw, z)
		if key == "bad" {
			e.UnknownDeclared = false
		}
		return e, notes, err
	}
	putRecords(t, s, t0, calEv(acctA, "bad", "2026-09-10T09:00:00Z", nil), calEv(acctA, "ok", "2026-09-10T10:00:00Z", nil))
	first := derive(t, s, t0)
	if first.Omissions[OmitCalendarRefused] != 1 || first.Counts.Refused != 1 || first.Counts.Events.Inserted != 1 {
		t.Fatalf("first: %v %+v", first.Omissions, first.Counts)
	}
	putRecords(t, s, t1, calEv(acctA, "other", "2026-09-11T09:00:00Z", nil))
	second := derive(t, s, t1)
	if second.Omissions[OmitCalendarRefused] != 1 || second.Counts.Refused != 0 {
		t.Fatalf("a later sync counts the standing loss, not a new refusal: %v %+v", second.Omissions, second.Counts)
	}
	if got := scalar(t, s, `select reason from calendar_losses where kind='refused'`); !strings.Contains(got, "UnknownDeclared") {
		t.Fatalf("reason %q", got)
	}
}

// The mapper's own refusal of an event reaches the report as a reason without the event's id.
func TestUnmappedReasonsCarryNoRecordContent(t *testing.T) {
	for in, want := range map[string]string{
		`calendar: refused event "abc" of source "teams": start time outside the years 0001 to 9999`: "event refused: start time outside the years 0001 to 9999",
		"not JSON: invalid character 'x' looking for beginning of value":                             "not JSON",
		"value is []interface {}, want an object":                                                    "value is not an object",
		"no recapId": "no recapId",
	} {
		if got := unmappedReason(&teamscal.UnmappedError{Reason: in}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if got := unmappedReason(errors.New("x")); got != "record could not be mapped" {
		t.Fatal(got)
	}
}

// A time zone change rebuilds, forgetting the covered days taken in the old zone.
func TestZoneChangeRebuildsAndForgetsOldCoveredDays(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// 23:30 UTC on the 10th is the 11th in Tokyo.
	putRecords(t, s, t0, calEv(acctA, "late", "2026-09-10T23:30:00Z", nil))
	if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, s, `select group_concat(day) from calendar_covered_days`); got != "2026-09-10" {
		t.Fatalf("utc: %q", got)
	}
	if res, err := s.EnsureCalendar(ctx, time.UTC, t1); err != nil || res != nil {
		t.Fatalf("an unchanged zone rebuilt: %v %v", res, err)
	}
	tokyo := time.FixedZone("JST", 9*3600)
	res, err := s.EnsureCalendar(ctx, tokyo, t2)
	if err != nil || res == nil {
		t.Fatalf("a changed zone did not rebuild: %v %v", res, err)
	}
	if got := scalar(t, s, `select group_concat(day) from calendar_covered_days`); got != "2026-09-11" {
		t.Fatalf("after the zone change the days are %q", got)
	}
	if zoneStamp(nil) != zoneStamp(time.UTC) || zoneStamp(tokyo) == zoneStamp(time.UTC) {
		t.Fatalf("stamps: %s %s %s", zoneStamp(nil), zoneStamp(time.UTC), zoneStamp(tokyo))
	}
}

// A current stamp over empty calendar tables, with calendar records present, rebuilds.
func TestEmptyCalendarTablesWithCurrentStampRebuild(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if res, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil || res == nil {
		t.Fatalf("first: %v %v", res, err)
	}
	// No records, current stamp, empty tables: nothing to rebuild.
	if res, err := s.EnsureCalendar(ctx, time.UTC, t1); err != nil || res != nil {
		t.Fatalf("empty archive: %v %v", res, err)
	}
	putRecords(t, s, t1, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil))
	res, err := s.EnsureCalendar(ctx, time.UTC, t1)
	if err != nil || res == nil || res.Counts.Events.Inserted != 1 {
		t.Fatalf("lost derivation: %+v %v", res, err)
	}
	if res, err := s.EnsureCalendar(ctx, time.UTC, t2); err != nil || res != nil {
		t.Fatalf("filled tables rebuilt again: %v %v", res, err)
	}
}

// A derivation that fails is rolled back to its savepoint, counted, and forgets the stamp so the next
// run derives again.
func TestDerivationFailureIsolatedInASavepoint(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "e1", "2026-09-10T09:00:00Z", nil))
	if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	putRecords(t, s, t1, calEv(acctA, "e1", "2026-09-10T09:00:00Z", map[string]any{"subject": "Renamed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}))
	if _, err := s.db.Exec(`create trigger refuse before update on calendar_source_events begin select raise(abort, 'refused'); end`); err != nil {
		t.Fatal(err)
	}
	res := derive(t, s, t1)
	if res.Omissions[OmitCalendarFailed] != 1 || res.Counts.Events.Seen != 0 || res.failure == nil {
		t.Fatalf("%+v", res)
	}
	if scalar(t, s, `select subject from calendar_source_events`) != "Subject e1" || scalar(t, s, `select count(*) from meta where key='calendar_derivation'`) != "0" {
		t.Fatal("the failed derivation left state behind")
	}
	if _, err := s.db.Exec(`drop trigger refuse`); err != nil {
		t.Fatal(err)
	}
	if res, err := s.EnsureCalendar(ctx, time.UTC, t2); err != nil || res == nil || res.Omissions[OmitCalendarFailed] != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := scalar(t, s, `select subject from calendar_source_events`); got != "Renamed" {
		t.Fatalf("the rebuild kept %q", got)
	}
	// A rebuild that fails is counted and not stamped either.
	if _, err := s.db.Exec(`delete from meta where key='calendar_derivation'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`create trigger refuse before update on calendar_source_events begin select raise(abort, 'refused'); end`); err != nil {
		t.Fatal(err)
	}
	res2, err := s.EnsureCalendar(ctx, time.UTC, t2)
	if err != nil || res2 == nil || res2.Omissions[OmitCalendarFailed] != 1 || scalar(t, s, `select count(*) from meta where key='calendar_derivation'`) != "0" {
		t.Fatalf("%+v %v", res2, err)
	}
}
