package syncer

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
	gl "github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func observationUTF16(s string, length bool) []byte {
	units := utf16.Encode([]rune(s))
	var b []byte
	if length {
		b = binary.AppendUvarint(b, uint64(len(units)))
	}
	for _, u := range units {
		b = append(b, byte(u>>8), byte(u))
	}
	return b
}

type observationCalendarRecord struct {
	key string
	raw []byte
}

// Reuse two committed calendar events on one day, not the fixture's mutable database.
func observationCalendarValues(t *testing.T) (string, [2]observationCalendarRecord) {
	t.Helper()
	srcs, _, err := teamsdesktop.Discover(fixtureRoot)
	if err != nil || len(srcs) != 1 {
		t.Fatalf("committed synthetic fixture discovery: sources=%d err=%v", len(srcs), err)
	}
	o, err := indexeddb.Open(srcs[0].LevelDBDir, srcs[0].BlobDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range dbs {
		if _, _, ok := teamsdesktop.ParseDatabaseName(db.Name); !ok {
			continue
		}
		for _, store := range db.Stores {
			if store.Name != "calendar" {
				continue
			}
			days := map[string][]observationCalendarRecord{}
			var pair [2]observationCalendarRecord
			err := o.Records(db.ID, store.ID, func(r indexeddb.Record) error {
				if pair[1].raw != nil || r.Err != nil || indexeddb.EnvelopeKind(r.Raw) == "blob" {
					return nil
				}
				s, ok := r.Key.(string)
				if !ok {
					return nil
				}
				value, err := o.Decode(db.ID, r.Raw)
				if err != nil {
					return nil
				}
				b, err := v8.Canonical(value)
				if err != nil {
					return err
				}
				var event map[string]any
				if err := json.Unmarshal(b, &event); err != nil {
					return nil
				}
				start, ok := event["startTime"].(string)
				if date, tagged := event["startTime"].(map[string]any); tagged {
					start, ok = date["$date"].(string)
				}
				if !ok || len(start) < 10 || strings.Contains(strings.ToLower(fmt.Sprint(event["eventType"])), "master") {
					return nil
				}
				day := start[:10]
				days[day] = append(days[day], observationCalendarRecord{s, append([]byte{1}, r.Raw...)})
				if len(days[day]) == 2 {
					copy(pair[:], days[day])
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if pair[1].raw != nil {
				return db.Name, pair
			}
		}
	}
	t.Fatal("fixture has no pair of plain calendar events with string keys on the same day")
	return "", [2]observationCalendarRecord{}
}

func observationSource(t *testing.T, journalDatabase bool) (string, string, string, map[string][]byte) {
	t.Helper()
	calendarDB, pair := observationCalendarValues(t)
	_, account, ok := teamsdesktop.ParseDatabaseName(calendarDB)
	if !ok {
		t.Fatal("calendar fixture account does not parse")
	}
	locale := "en-gb"
	if account.Locale == locale {
		locale = "en-us"
	}
	keeperDB := "Teams:calendar:react-web-client:" + account.TenantID + ":" + account.UserID + ":" + locale
	root := filepath.Join(t.TempDir(), "EBWebView")
	dir := filepath.Join(root, "WV2Profile_fixture", "IndexedDB", "https_teams.microsoft.com_0.indexeddb.leveldb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	put := func(k, v []byte) {
		t.Helper()
		if err := db.Put(k, v, &opt.WriteOptions{Sync: true}); err != nil {
			t.Fatal(err)
		}
	}
	nameKey := func(name string) []byte {
		key := append([]byte{0, 0, 0, 0, 201}, observationUTF16("https://teams.microsoft.com", true)...)
		return append(key, observationUTF16(name, true)...)
	}
	dataKey := func(id byte, key string) []byte {
		return append([]byte{0, id, 1, 1, 1}, observationUTF16(key, true)...)
	}
	put(nameKey(keeperDB), []byte{1})
	put([]byte{0, 1, 0, 0, 50, 1, 0}, observationUTF16("calendar", false))
	put(dataKey(1, pair[1].key), pair[1].raw)
	put([]byte{0, 2, 0, 0, 50, 1, 0}, observationUTF16("calendar", false))
	if journalDatabase {
		put(dataKey(2, pair[0].key), pair[0].raw)
	} else {
		put(nameKey(calendarDB), []byte{2})
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if journalDatabase {
		put(nameKey(calendarDB), []byte{2})
	} else {
		put(dataKey(2, pair[0].key), pair[0].raw)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	saved := map[string][]byte{}
	for _, path := range logs {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 7 {
			saved[path] = b
		}
	}
	if len(saved) == 0 {
		t.Fatal("synthetic setup produced no journal payload")
	}
	return root, calendarDB, keeperDB, saved
}

func observationState(t *testing.T, archive, query string) string {
	t.Helper()
	rows, err := openRaw(t, archive).Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var state []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		state = append(state, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(state, "\n")
}

// A committed readable digest must not strand changed calendar content after recovery.
func TestSyncIncompleteObservationReplaysReadableCalendarChange(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery-full=%t", full), func(t *testing.T) {
			tmp := isolateTmp(t)
			root, _, keeper, initialLogs := observationSource(t, false)
			archive := newDB(t)
			opts := Options{Root: root, DBPath: archive, FullRead: true}
			first, _ := run(t, opts)
			if first.Omissions["truncated_log_tail"] != 0 ||
				recordsQuery(t, archive, `select count(*) from calendar_source_events where removed_at is null`) != 2 ||
				recordsQuery(t, archive, `select count(*) from meta where key='calendar_derivation' and value<>''`) != 1 {
				t.Fatal("fixture must start with complete current-stamped nonempty calendar")
			}
			_, pair := observationCalendarValues(t)
			var dir string
			for path := range initialLogs {
				dir = filepath.Dir(path)
			}
			o, err := indexeddb.Open(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			value, decodeErr := o.Decode(1, pair[1].raw[1:])
			closeErr := o.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatal("fixture decode/close", decodeErr, closeErr)
			}
			canonical, err := v8.Canonical(value)
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(canonical, &event); err != nil {
				t.Fatal(err)
			}
			subject, ok := event["subject"].(string)
			if !ok || subject == "" || bytes.Count(pair[1].raw, []byte(subject)) != 1 {
				t.Fatal("qualification: fixture needs one plain serialized subject")
			}
			want := strings.Repeat("Z", len(subject))
			if want == subject {
				t.Fatal("qualification: replacement must change readable content")
			}
			changed := bytes.Replace(pair[1].raw, []byte(subject), []byte(want), 1)
			db, err := gl.OpenFile(dir, &opt.Options{Compression: opt.NoCompression})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			key := func(id byte, s string) []byte {
				return append([]byte{0, id, 1, 1, 1}, observationUTF16(s, true)...)
			}
			if err := db.Put(key(1, pair[1].key), changed, &opt.WriteOptions{Sync: true}); err != nil {
				t.Fatal(err)
			}
			if err := db.CompactRange(util.Range{}); err != nil {
				t.Fatal(err)
			}
			// A distinct journal tail is damage evidence; changed content is in a table.
			if err := db.Put(key(2, pair[0].key), pair[0].raw, &opt.WriteOptions{Sync: true}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			paths, err := filepath.Glob(filepath.Join(dir, "*.log"))
			if err != nil {
				t.Fatal(err)
			}
			logs := map[string][]byte{}
			for _, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(b) > 7 {
					logs[path] = b
					if err := os.WriteFile(path, b[:7], 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(logs) == 0 {
				t.Fatal("qualification: no owned journal payload to tear")
			}
			const sources = `select json_array(source,account_id,window_start,window_end,synced_at,cache_fresh_at) from calendar_sources order by source,account_id`
			const days = `select json_array(source,account_id,day,first_verified_at,last_verified_at) from calendar_covered_days order by source,account_id,day`
			beforeSources, beforeDays := observationState(t, archive, sources), observationState(t, archive, days)
			if beforeSources == "" || beforeDays == "" {
				t.Fatal("qualification: baseline must already have source freshness and covered days")
			}
			for repeat := 0; repeat < 2; repeat++ {
				damaged, _ := run(t, opts)
				if damaged.Omissions["truncated_log_tail"] == 0 || damaged.Status == "ok" {
					t.Fatalf("qualification: damaged read lacks actual torn-tail refusal: %+v", damaged)
				}
				if n := recordsQuery(t, archive, `select count(*) from records where database=? and json_extract(value_json,'$.subject')=? and removed_at is null`, keeper, want); n != 1 {
					t.Fatalf("qualification: readable changed value not committed: %d", n)
				}
				if observationState(t, archive, sources) != beforeSources || observationState(t, archive, days) != beforeDays ||
					recordsQuery(t, archive, `select count(*) from calendar_source_events where removed_at is not null`) != 0 ||
					recordsQuery(t, archive, `select count(*) from records where removed_at is not null`) != 0 {
					t.Fatal("incomplete positive content granted absence/freshness authority")
				}
			}
			const rawState = `select json_array(database,key_json,content_hash,updated_at,removed_at) from records order by database,key_json`
			committed := observationState(t, archive, rawState)
			for path, b := range logs {
				if err := os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			opts.FullRead = full
			restored, _ := run(t, opts)
			if restored.Omissions["truncated_log_tail"] != 0 || restored.Status != "ok" {
				t.Fatalf("qualification: restoration remains torn: %+v", restored)
			}
			if observationState(t, archive, rawState) != committed {
				t.Fatal("qualification: recovery changed raw digest/timestamp instead of replaying identical content")
			}
			if n := recordsQuery(t, archive, `select count(*) from calendar_source_events where subject=? and removed_at is null`, want); n != 1 {
				t.Errorf("readable_calendar_change_stranded_after_identical_recovery full=%t count=%d", full, n)
			}
			if recordsQuery(t, archive, `select count(*) from calendar_source_events where removed_at is not null`) != 0 {
				t.Fatal("restoration invented a gone event")
			}
			if leaked := snapshotDirs(t, tmp); len(leaked) != 0 {
				t.Errorf("owned snapshots leaked: %v", leaked)
			}
		})
	}
}
func TestSyncIncompleteObservationRetainsArchiveAndCalendar(t *testing.T) {
	for _, journalDatabase := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal-database=%t", journalDatabase), func(t *testing.T) {
			tmp := isolateTmp(t)
			root, calendarDB, keeperDB, logs := observationSource(t, journalDatabase)
			archive := newDB(t)
			opts := Options{Root: root, DBPath: archive, FullRead: true}
			first, _ := run(t, opts)
			if first.Omissions["truncated_log_tail"] != 0 {
				t.Fatalf("good setup was incomplete: %+v", first)
			}
			if n := recordsQuery(t, archive, `select count(*) from records where database=? and value_json is not null and removed_at is null`, calendarDB); n != 1 {
				t.Fatalf("good setup did not archive the journal-only calendar record: %d", n)
			}
			const recordSQL = `select json_array(rowid,source,database,store,key_json,value_json,content_hash,removed_at) from records order by source,database,store,key_json`
			const eventSQL = `select json_array(source,account_id,event_key,subject,removed_at) from calendar_source_events order by source,account_id,event_key`
			const sourceSQL = `select json_array(source,account_id,window_start,window_end,synced_at,cache_fresh_at) from calendar_sources order by source,account_id`
			const daySQL = `select json_array(source,account_id,day,first_verified_at,last_verified_at) from calendar_covered_days order by source,account_id,day`
			goodRecords := observationState(t, archive, recordSQL)
			goodEvents := observationState(t, archive, eventSQL)
			if goodEvents == "" || observationState(t, archive, daySQL) == "" || observationState(t, archive, sourceSQL) == "" {
				t.Fatal("setup did not project a calendar event and positively cover its day")
			}
			if n := recordsQuery(t, archive, `select count(*) from calendar_source_events where removed_at is null`); n != 2 {
				t.Fatalf("setup did not project two distinct events on the retained day: %d", n)
			}
			if n := recordsQuery(t, archive, `select count(*) from calendar_source_events where removed_at is not null`); n != 0 {
				t.Fatalf("good setup already has gone events: %d", n)
			}
			var dir string
			for path := range logs {
				dir = filepath.Dir(path)
			}
			for cycle := 0; cycle < 2; cycle++ {
				goodSources := observationState(t, archive, sourceSQL)
				goodDays := observationState(t, archive, daySQL)
				for path, b := range logs {
					if err := os.WriteFile(path, b[:7], 0o600); err != nil {
						t.Fatal(err)
					}
				}
				o, err := indexeddb.Open(dir, "")
				if err != nil {
					t.Fatal(err)
				}
				stats := o.Stats()
				if err := o.Close(); err != nil {
					t.Fatal(err)
				}
				if stats.TruncatedLogTails == 0 {
					t.Fatal("setup did not create a positively detected torn journal")
				}
				_, dbs, err := indexeddb.Census(dir)
				if err != nil {
					t.Fatal(err)
				}
				hasCalendar := false
				for _, db := range dbs {
					hasCalendar = hasCalendar || db.Name == calendarDB
				}
				if hasCalendar == journalDatabase {
					t.Fatalf("fixture did not hide the intended key/database: journalDatabase=%t census=%+v", journalDatabase, dbs)
				}
				for repeat := 0; repeat < 2; repeat++ {
					report, _ := run(t, opts)
					if report.Omissions["truncated_log_tail"] == 0 || report.Status == "ok" {
						t.Errorf("cycle=%d repeat=%d incomplete observation reported success: %+v", cycle, repeat, report)
					}
					if n := recordsQuery(t, archive, `select count(*) from records where database=? and value_json is not null and removed_at is null`, keeperDB); n != 1 {
						t.Errorf("readable prefix lost: count=%d", n)
					}
					if got := observationState(t, archive, recordSQL); got != goodRecords {
						t.Errorf("cycle=%d repeat=%d incomplete observation changed archived values/rowids/tombstones\nwant %s\ngot  %s", cycle, repeat, goodRecords, got)
					}
					for _, check := range []struct{ name, sql, want string }{
						{"events/no false gone", eventSQL, goodEvents},
						{"source freshness", sourceSQL, goodSources},
						{"day verification", daySQL, goodDays},
					} {
						if got := observationState(t, archive, check.sql); got != check.want {
							t.Errorf("cycle=%d repeat=%d incomplete observation changed %s\nwant %s\ngot  %s", cycle, repeat, check.name, check.want, got)
						}
					}
				}
				for path, b := range logs {
					if err := os.WriteFile(path, b, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				restored, _ := run(t, opts)
				if restored.Omissions["truncated_log_tail"] != 0 {
					t.Errorf("restored observation still incomplete: %+v", restored)
				}
				if got := observationState(t, archive, recordSQL); got != goodRecords {
					t.Errorf("restoration changed archived values/rowids/tombstones\nwant %s\ngot  %s", goodRecords, got)
				}
				if got := observationState(t, archive, eventSQL); got != goodEvents {
					t.Errorf("restoration changed calendar event identity/gone state\nwant %s\ngot  %s", goodEvents, got)
				}
			}
			if leaked := snapshotDirs(t, tmp); len(leaked) != 0 {
				t.Errorf("owned snapshots leaked: %v", leaked)
			}
		})
	}
}
