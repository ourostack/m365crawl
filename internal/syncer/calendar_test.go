package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// utcDays makes the sync group days in UTC, so the expected windows do not depend on the machine.
func utcDays(t *testing.T) {
	t.Helper()
	old := calendarZone
	calendarZone = func() *time.Location { return time.UTC }
	t.Cleanup(func() { calendarZone = old })
}

func queryStr(t *testing.T, db, q string, args ...any) string {
	t.Helper()
	var s *string
	if err := openRaw(t, db).QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if s == nil {
		return ""
	}
	return *s
}

// Syncing the fixture fills the calendar tables from its calendar, catch-up and recap records.
func TestSyncCalendarFromFixture(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	c := r.Calendar
	if c.Events != (store.Counts{Seen: 28, Inserted: 28}) || c.Recaps != (store.Counts{Seen: 12, Inserted: 10, Updated: 2}) || c.RecapItems != (store.Counts{Seen: 16, Inserted: 16}) ||
		c.Gone != 0 || c.Linked != 2 || c.Refused != 0 {
		t.Fatalf("calendar counts: %+v", c)
	}
	if r.Sources[0].Counts.Calendar != c {
		t.Fatalf("source counts %+v", r.Sources[0].Counts.Calendar)
	}
	if r.Status != StatusOK || r.Omissions["calendar_unknown_time_zone"] != 2 {
		t.Fatalf("status %s omissions %v", r.Status, r.Omissions)
	}
	for q, want := range map[string]int{
		`select count(*) from calendar_source_events`:                              28,
		`select count(*) from calendar_source_events where removed_at is not null`: 0,
		`select count(*) from calendar_sources`:                                    2,
		`select count(*) from calendar_covered_days`:                               14,
		`select count(*) from calendar_recaps`:                                     10,
		`select count(*) from calendar_recap_items`:                                16,
		`select count(*) from calendar_account_links`:                              0,
		`select count(*) from calendar_matches`:                                    0,
		`select count(*) from calendar_recaps where ical_uid<>''`:                  10,
		`select count(*) from records where database like 'Teams:calendar:%'`:      34,
		`select count(*) from records where store='meeting-recap-catchup'`:         4,
	} {
		if got := recordsQuery(t, db, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	// The sync's own record carries the counts too.
	if counts := queryStr(t, db, `select counts_json from sync_runs where source<>'' order by id desc limit 1`); !strings.Contains(counts, `"calendar":{"events":{"seen":28,"inserted":28`) {
		t.Fatalf("counts_json %s", counts)
	}
	// sql works on the archive, as the plan asks.
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events`); n != 28 {
		t.Fatal(n)
	}
}

func TestSyncCalendarSecondSyncMapsNothing(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	snapshot := func() string {
		return queryStr(t, db, `select group_concat(x, char(10)) from (select source_id||'|'||seen_at||'|'||subject||'|'||coalesce(removed_at,'') x from calendar_source_events order by source_id, account_id)`)
	}
	before := snapshot()
	// Not even the fingerprint shortcut: every record is read again, and it is the same record.
	again, _ := run(t, Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if c := again.Calendar; c.Events.Seen != 0 || c.Recaps.Seen != 0 || c.RecapItems.Seen != 0 || c.Gone != 0 {
		t.Fatalf("a second sync mapped %+v", c)
	}
	if after := snapshot(); after != before {
		t.Fatalf("calendar rows changed:\n%s\n---\n%s", before, after)
	}
	// And with the fingerprint shortcut the source is not even read.
	unchanged, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if unchanged.Status != StatusUnchanged || unchanged.Calendar.Events.Seen != 0 {
		t.Fatalf("status %s calendar %+v", unchanged.Status, unchanged.Calendar)
	}
}

// With the skip machinery of the sync itself (an unchanged record is neither re-read nor
// rewritten), a second sync maps no calendar row, and the window and the covered days stay.
func TestSyncCalendarWithRecordSkipping(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := fixtureCopy(t)
	runs := watchApplies(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	days := queryStr(t, db, `select group_concat(day) from (select day from calendar_covered_days order by account_id, day)`)
	window := queryStr(t, db, `select group_concat(window_start||window_end) from (select * from calendar_sources order by account_id)`)
	touchLog(t, root)
	again, _ := run(t, Options{Root: root, DBPath: db})
	if r := (*runs)[1]; r.genericSkips != fixtureRecords {
		t.Fatalf("the second sync skipped %+v", r)
	}
	if c := again.Calendar; c.Events.Seen != 0 || c.Recaps.Seen != 0 || c.Gone != 0 {
		t.Fatalf("calendar counts %+v", c)
	}
	if got := queryStr(t, db, `select group_concat(day) from (select day from calendar_covered_days order by account_id, day)`); got != days {
		t.Fatalf("covered days %s, want %s", got, days)
	}
	if got := queryStr(t, db, `select group_concat(window_start||window_end) from (select * from calendar_sources order by account_id)`); got != window {
		t.Fatalf("window %s, want %s", got, window)
	}
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events where removed_at is not null`); n != 0 {
		t.Fatalf("%d events marked gone", n)
	}
}

// fakeCache stands in for the generic read: it hands the sync records of its own, and, like the
// skip machinery, can leave unchanged ones unread while still reporting them seen.
type fakeCache struct {
	dbs  map[string][]teamsdesktop.GenericRecord
	skip func(teamsdesktop.GenericRecord) bool
}

func (f *fakeCache) install(t *testing.T) {
	t.Helper()
	old := readGenericFn
	t.Cleanup(func() { readGenericFn = old })
	readGenericFn = func(_ context.Context, _ string, _ *teamsdesktop.Account, _ int64, opts teamsdesktop.GenericOptions, emit func(teamsdesktop.GenericRecord) error) (teamsdesktop.GenericResult, error) {
		res := teamsdesktop.GenericResult{Omissions: map[string]int{}, Complete: map[string]bool{}}
		names := make([]string, 0, len(f.dbs))
		for n := range f.dbs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			seen := map[string]map[string]struct{}{}
			for _, rec := range f.dbs[name] {
				if seen[rec.Store] == nil {
					seen[rec.Store] = map[string]struct{}{}
				}
				seen[rec.Store][string(rec.KeyJSON)] = struct{}{}
				if f.skip != nil && f.skip(rec) {
					continue
				}
				if err := emit(rec); err != nil {
					return res, err
				}
			}
			res.Present, res.Complete[name] = append(res.Present, name), true
			if err := opts.OnDatabase(name, true, seen); err != nil {
				return res, err
			}
		}
		return res, nil
	}
}

func calJSON(id, start string, extra map[string]any) []byte {
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

const fakeCalendarDB = "Teams:calendar:react-web-client:00000000-0000-4000-8000-000000000001:00000000-0000-4000-8000-0000000000a1:en-us"

func fakeEvent(id, start string, extra map[string]any) teamsdesktop.GenericRecord {
	a := acctA
	return teamsdesktop.GenericRecord{Account: &a, Database: fakeCalendarDB, Store: "calendar", KeyJSON: []byte(`"` + id + `"`), ValueJSON: calJSON(id, start, extra)}
}

func rich() map[string]any {
	return map[string]any{
		"bodyContent": "<p>Agenda</p>", "bodyContentType": "html",
		"attendees": []any{map[string]any{"name": "Alex Fixture", "address": "alex@example.invalid", "type": "Required"}, map[string]any{"name": "Sam Fixture", "address": "sam@example.invalid", "type": "Optional"}},
	}
}

// A later copy of an event that Teams fetched without its attendees does not erase them.
func TestSyncThinnerCalendarCopyKeepsAttendees(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {fakeEvent("e1", "2026-09-10T09:00:00Z", rich())}}}
	cache.install(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events where attendees_json like '%Alex Fixture%' and body_html='<p>Agenda</p>'`); n != 1 {
		t.Fatal("the rich copy was not stored")
	}
	cache.dbs[fakeCalendarDB] = []teamsdesktop.GenericRecord{fakeEvent("e1", "2026-09-10T09:00:00Z", map[string]any{"lastModifiedTime": map[string]any{"$date": "2026-08-03T00:00:00Z"}})}
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if r.Calendar.Events.Seen != 1 {
		t.Fatalf("calendar counts %+v", r.Calendar)
	}
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events where attendees_json like '%Alex Fixture%' and body_html='<p>Agenda</p>' and last_modified='2026-08-03T00:00:00.000Z'`); n != 1 {
		t.Fatal("a thinner, newer copy erased the attendees or did not move the schedule")
	}
	// The records table holds only the thin copy: the calendar remembers more than it does.
	if n := recordsQuery(t, db, `select count(*) from records where store='calendar' and value_json like '%Alex Fixture%'`); n != 0 {
		t.Fatal("the records row kept the rich copy")
	}
}

// Only changed records are mapped when the sync skips the unchanged ones; the days the cache holds
// still count, a removed event on a held day is gone, and unchanged events keep their detail.
func TestSyncCalendarSkippingMapsOnlyChangedRecords(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	e1 := fakeEvent("e1", "2026-09-10T09:00:00Z", rich())
	e2 := fakeEvent("e2", "2026-09-10T11:00:00Z", nil)
	e3 := fakeEvent("e3", "2026-09-14T09:00:00Z", nil)
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {e1, e2, e3}}}
	cache.install(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	window := queryStr(t, db, `select window_start||window_end from calendar_sources`)

	// Before sync 2: e2 changes, e3 leaves the cache (its day, the 14th, goes with it), and e1 is
	// unchanged, so the sync neither reads nor rewrites it.
	e2.ValueJSON = calJSON("e2", "2026-09-10T11:00:00Z", map[string]any{"subject": "Renamed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}})
	e4 := fakeEvent("e4", "2026-09-10T15:00:00Z", nil)
	cache.dbs[fakeCalendarDB] = []teamsdesktop.GenericRecord{e1, e2, e4}
	cache.skip = func(r teamsdesktop.GenericRecord) bool { return string(r.KeyJSON) == `"e1"` }
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if c := r.Calendar.Events; c.Seen != 2 || c.Inserted != 1 || c.Updated != 1 {
		t.Fatalf("events %+v", c)
	}
	if got := queryStr(t, db, `select subject from calendar_source_events where source_id='e2'`); got != "Renamed" {
		t.Fatalf("subject %q", got)
	}
	if got := queryStr(t, db, `select attendees_json from calendar_source_events where source_id='e1'`); !strings.Contains(got, "Alex Fixture") {
		t.Fatalf("an unchanged event lost its detail: %q", got)
	}
	// e3 was on a day the cache no longer holds: eviction, so it stays live and the days stay.
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events where removed_at is not null`); n != 0 {
		t.Fatalf("%d events marked gone on an evicted day", n)
	}
	if got := queryStr(t, db, `select group_concat(day) from (select day from calendar_covered_days order by day)`); got != "2026-09-10,2026-09-14" {
		t.Fatalf("covered days %q", got)
	}
	if got := queryStr(t, db, `select window_start||window_end from calendar_sources`); got != window {
		t.Fatalf("window %q, was %q", got, window)
	}

	// Sync 3: e4 is deleted on the 10th, which the cache still holds: gone, with its data.
	cache.dbs[fakeCalendarDB] = []teamsdesktop.GenericRecord{e1, e2}
	r, _ = run(t, Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if r.Calendar.Gone != 1 || r.Calendar.Events.Seen != 0 {
		t.Fatalf("calendar %+v", r.Calendar)
	}
	if got := queryStr(t, db, `select group_concat(source_id) from calendar_source_events where removed_at is not null`); got != "e4" {
		t.Fatalf("gone: %q", got)
	}
	if got := queryStr(t, db, `select subject from calendar_source_events where source_id='e4'`); got != "Subject e4" {
		t.Fatalf("a gone event lost its data: %q", got)
	}
}

// An unmapped record is a counted loss, never fatal, and the messages of the same sync are kept.
func TestSyncCalendarUnmappedRecordIsAnOmissionNotAFailure(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	a := acctA
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {
		fakeEvent("e1", "2026-09-10T09:00:00Z", nil),
		{Account: &a, Database: fakeCalendarDB, Store: "calendar", KeyJSON: []byte(`"broken"`), ValueJSON: []byte(`{"objectId":"broken"}`)},
	}}}
	cache.install(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != StatusOmissions || r.Omissions["calendar_unmapped"] != 1 || r.Calendar.Events.Inserted != 1 || r.Messages.Inserted != fixtureMessages {
		t.Fatalf("status %s omissions %v calendar %+v", r.Status, r.Omissions, r.Calendar)
	}
}

// A calendar write that fails fails the source like any write error, and nothing of it is kept.
func TestSyncCalendarWriteFailureFailsTheSource(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {fakeEvent("e1", "2026-09-10T09:00:00Z", nil)}}}
	cache.install(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	if _, err := openRaw(t, db).Exec(`create trigger refuse before update on calendar_source_events begin select raise(abort, 'calendar refused'); end`); err != nil {
		t.Fatal(err)
	}
	cache.dbs[fakeCalendarDB] = []teamsdesktop.GenericRecord{fakeEvent("e1", "2026-09-10T09:00:00Z", map[string]any{"subject": "Renamed", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}})}
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if err == nil || !strings.Contains(err.Error(), "calendar refused") {
		t.Fatalf("err = %v", err)
	}
	if got := queryStr(t, db, `select subject from calendar_source_events`); got != "Subject e1" {
		t.Fatalf("a failed source kept %q", got)
	}
	if n := recordsQuery(t, db, `select count(*) from records where store='calendar' and value_json like '%Renamed%'`); n != 0 {
		t.Fatal("the records of a failed source were kept")
	}
}

// An archive upgraded from schema 4 (or whose calendar was derived by another mapper) fills its
// calendar from the records it holds, before any source is read.
func TestRunBackfillsCalendarFromRecordsWithoutTheCache(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	first, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	d := openRaw(t, db)
	for _, q := range []string{`delete from calendar_recap_items`, `delete from calendar_recaps`, `delete from calendar_covered_days`, `delete from calendar_sources`, `delete from calendar_source_events`, `delete from meta where key='calendar_derivation'`} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// The cache is gone, and the fingerprint says nothing changed anyway.
	again, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if again.Status != StatusUnchanged || again.Calendar.Events != first.Calendar.Events || again.Calendar.RecapItems != first.Calendar.RecapItems {
		t.Fatalf("status %s calendar %+v, first %+v", again.Status, again.Calendar, first.Calendar)
	}
	if again.Omissions["calendar_unknown_time_zone"] != 2 {
		t.Fatalf("omissions %v", again.Omissions)
	}
	if n := recordsQuery(t, db, `select count(*) from calendar_source_events`); n != 28 {
		t.Fatalf("%d events", n)
	}
	// Once done, it is not done again.
	third, _ := run(t, Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if third.Calendar.Events.Seen != 0 {
		t.Fatalf("the backfill ran twice: %+v", third.Calendar)
	}
}

func TestRunFailsWhenTheCalendarBackfillFails(t *testing.T) {
	isolateTmp(t)
	old := ensureCalendar
	t.Cleanup(func() { ensureCalendar = old })
	ensureCalendar = func(context.Context, *store.Store, time.Time) (*store.CalendarResult, error) {
		return nil, errors.New("backfill refused")
	}
	db := newDB(t)
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError || !strings.Contains(err.Error(), "backfill refused") {
		t.Fatalf("err = %v", err)
	}
	if st := readStatus(t, db); st.LastRun == nil || st.LastRun.Status != StatusFailed {
		t.Fatalf("last run %+v", st.LastRun)
	}
}

func TestEnsureCalendarSeamUsesTheSyncZone(t *testing.T) {
	st, err := store.Open(context.Background(), newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	res, err := ensureCalendar(context.Background(), st, time.Now())
	if err != nil || res == nil {
		t.Fatalf("ensureCalendar = %v, %v", res, err)
	}
	if calendarZone() != time.Local {
		t.Fatal("the sync groups days in the machine's zone")
	}
}

func TestLostIgnoresEventsThatAreOnlyLessPrecise(t *testing.T) {
	if n := lost(map[string]int{"calendar_unknown_time_zone": 3, "calendar_all_day_unaligned": 2, "denied_store": 1}); n != 0 {
		t.Fatalf("lost = %d", n)
	}
	if n := lost(map[string]int{"calendar_unmapped": 1, "calendar_refused": 2, "calendar_unknown_time_zone": 3}); n != 3 {
		t.Fatalf("lost = %d", n)
	}
}
