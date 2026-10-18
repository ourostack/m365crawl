package syncer

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestCalendarReplayPendingReadFailureFailsBeforeSnapshot(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {fakeEvent("e1", "2026-09-10T09:00:00Z", nil)}}}
	cache.install(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	intact, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if intact.Status != StatusUnchanged {
		t.Fatalf("intact metadata did not qualify unchanged control: %+v", intact)
	}
	const stateSQL = `select json_array('records',rowid,database,store,key_json,value_json,removed_at) from records
		union all select json_array('events',rowid,account_id,source_id,subject,removed_at) from calendar_source_events
		union all select json_array('sources',rowid,account_id,synced_at,cache_fresh_at) from calendar_sources
		union all select json_array('days',rowid,account_id,day,last_verified_at) from calendar_covered_days order by 1`
	before := observationState(t, db, stateSQL)
	discover := discoverSources
	t.Cleanup(func() { discoverSources = discover })
	armed := false
	discoverSources = func(root string) ([]teamsdesktop.Source, []string, error) {
		sources, others, err := discover(root)
		if err == nil {
			if _, err := openRaw(t, db).Exec(`drop table meta`); err != nil {
				t.Fatal(err)
			}
			armed = true
		}
		return sources, others, err
	}
	cache.skip = func(teamsdesktop.GenericRecord) bool {
		t.Fatal("uncertain replay metadata must fail before reading source records")
		return false
	}
	report, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	if !armed || err == nil || report.Status != StatusFailed || len(report.Sources) != 1 ||
		report.Sources[0].Error == nil || report.Sources[0].Error.Code != "db_error" || !strings.Contains(err.Error(), "meta") {
		t.Fatalf("pending-query failure was hidden or occurred outside the qualified source stage: %+v %v armed=%t", report, err, armed)
	}
	if after := observationState(t, db, stateSQL); after != before {
		t.Fatalf("failed source changed raw/projection/authority after intentional private metadata fault:\nbefore %s\nafter %s", before, after)
	}
}

func TestLegacyFingerprintShortcutStillReportsCalendarLosses(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	cache := &fakeCache{dbs: map[string][]teamsdesktop.GenericRecord{fakeCalendarDB: {fakeEvent("e1", "2026-09-10T09:00:00Z", nil)}}}
	cache.install(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	fingerprint := queryStr(t, db, `select fingerprint from sync_runs where source=? order by rowid desc limit 1`, sourceKey)
	if fingerprint == "" {
		t.Fatal("legacy fixture requires a real qualified source fingerprint")
	}
	clean, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if clean.Status != StatusUnchanged || clean.Sources[0].Counts != nil {
		t.Fatalf("no-loss shortcut control: %+v", clean)
	}
	a := acctA
	cache.dbs[fakeCalendarDB] = append(cache.dbs[fakeCalendarDB],
		teamsdesktop.GenericRecord{Account: &a, Database: fakeCalendarDB, Store: "calendar", KeyJSON: []byte(`"unmapped"`), ValueJSON: []byte(`{"objectId":"unmapped"}`)})
	loss, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db, FullRead: true})
	if err != nil || loss.Status != StatusOmissions || loss.Omissions["calendar_unmapped"] != 1 {
		t.Fatalf("real retained loss qualification: %+v %v", loss, err)
	}
	if got := queryStr(t, db, `select fingerprint from sync_runs where source=? order by rowid desc limit 1`, sourceKey); got != "" {
		t.Fatalf("current policy minted an incomplete reusable fingerprint: %q", got)
	}
	if n := recordsQuery(t, db, `select count(*) from meta where key like 'calendar_replay/%'`); n != 0 {
		t.Fatalf("legacy loss fixture unexpectedly has %d pending obligations", n)
	}
	// Pre-fix archives retained this actual source fingerprint despite counted losses.
	if _, err := openRaw(t, db).Exec(`update sync_runs set fingerprint=? where rowid=(select max(rowid) from sync_runs where source=?)`, fingerprint, sourceKey); err != nil {
		t.Fatal(err)
	}
	const stateSQL = `select json_array('records',rowid,database,store,key_json,value_json,removed_at) from records
		union all select json_array('events',rowid,account_id,source_id,subject,removed_at) from calendar_source_events
		union all select json_array('sources',rowid,account_id,synced_at,cache_fresh_at) from calendar_sources
		union all select json_array('days',rowid,account_id,day,first_verified_at,last_verified_at) from calendar_covered_days order by 1`
	before := observationState(t, db, stateSQL)
	cache.skip = func(teamsdesktop.GenericRecord) bool {
		t.Fatal("legacy fingerprint discriminator did not take the unchanged-source shortcut")
		return false
	}
	again, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	if err != nil || again.Status != StatusOmissions || again.Omissions["calendar_unmapped"] != 1 ||
		len(again.Sources) != 1 || again.Sources[0].Status != StatusOmissions || again.Sources[0].Counts != nil {
		t.Fatalf("legacy shortcut hid retained calendar losses: %+v %v", again, err)
	}
	if after := observationState(t, db, stateSQL); after != before {
		t.Fatalf("legacy shortcut rewrote retained raw/calendar content or authority:\nbefore %s\nafter %s", before, after)
	}
}
