//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// The binary reads the calendar of the fixture: an agenda, one event of it, and the empty answer a
// machine that never synced gets.
func TestE2ECalendar(t *testing.T) {
	e := newEnv(t)
	before := mustJSON(t, e.cmd("calendar", "--max-age", "0").stdout)
	if before["needs_sync"] != true || before["coverage_gap"] != true {
		t.Fatalf("before a sync: %v", before)
	}
	e.sync()
	day := ok(t, e.cmd("calendar", "--from", "2023-11-22T00:00:00Z", "--days", "1", "--account", "00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1"))
	items, _ := day["items"].([]any)
	if len(items) != 2 || day["coverage_gap"] != false {
		t.Fatalf("agenda: %v", day)
	}
	first := items[0].(map[string]any)
	id, _ := first["event_id"].(string)
	if id == "" || first["sources"] == nil || first["detail_level"] == nil {
		t.Fatalf("item: %v", first)
	}
	ev := ok(t, e.cmd("calendar", "event", id, "--account", "00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1"))
	if ev["event_id"] != id || ev["subject"] != first["subject"] {
		t.Fatalf("event: %v", ev)
	}
	res := e.cmd("calendar", "event", "ev_nothing", "--max-age", "0")
	mustExit(t, res, 2)
}

// `calendar sources` on the fixture archive reports each Teams account's covered days, detail
// counts, recap counts and the zone name no IANA id exists for; with the Outlook fixture synced it
// adds the Outlook row, and `doctor` reports the Outlook source without failing.
func TestE2ECalendarSources(t *testing.T) {
	e := newEnv(t)
	before := mustJSON(t, e.cmd("calendar", "sources", "--max-age", "0").stdout)
	if before["needs_sync"] != true {
		t.Fatalf("before a sync: %v", before)
	}
	e.sync()
	res := ok(t, e.cmd("calendar", "sources", "--account", account1))
	rows, _ := res["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows: %v", res)
	}
	r := rows[0].(map[string]any)
	if r["source"] != "teams" || r["covered_days"] != float64(7) || r["events_live"] != float64(14) || r["events_with_detail"] != float64(1) ||
		r["recaps_with_content"] != float64(3) || r["link"] != "none" || r["last_verified_at"] == nil {
		t.Fatalf("row: %v", r)
	}
	if z, _ := r["unknown_time_zones"].([]any); len(z) != 1 || z[0] != "FixtureUnknownSt" {
		t.Fatalf("zones: %v", r["unknown_time_zones"])
	}

	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../testdata/outlook-fixture/HxStore.hxd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // G703: a path under the test temp dir
		t.Fatal(err)
	}
	mustExit(t, e.cmd("--outlook-root", root, "sync"), 0)
	all := ok(t, e.cmd("--outlook-root", root, "calendar", "sources", "--max-age", "0"))
	var outlook map[string]any
	for _, it := range all["items"].([]any) {
		if m := it.(map[string]any); m["source"] == "outlook" {
			outlook = m
		}
	}
	if outlook == nil || outlook["status"] == nil || outlook["deletions"] != "unverified" || outlook["read_interval_seconds"] != float64(300) || outlook["covered_days"] == float64(0) {
		t.Fatalf("outlook row: %v", all)
	}
	doc := ok(t, e.cmd("--outlook-root", root, "doctor"))
	var found bool
	for _, c := range doc["checks"].([]any) {
		if m := c.(map[string]any); m["name"] == "outlook_store" {
			found = m["ok"] == true
		}
	}
	if !found {
		t.Fatalf("doctor: %v", doc)
	}
}

// The binary lists the action items of the fixture meeting and links its chat to the calendar.
func TestE2ECalendarActionsAndConversations(t *testing.T) {
	e := newEnv(t)
	e.sync()
	acct := "00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1"
	mine := ok(t, e.cmd("calendar", "actions", "--from", "2023-11-20", "--days", "1", "--mine", "--account", acct))
	items, _ := mine["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("actions: %v", mine)
	}
	first := items[0].(map[string]any)
	if first["owner"] != "Alex Fixture" || first["mine"] != true {
		t.Fatalf("action: %v", first)
	}
	convs := ok(t, e.cmd("conversations", "--kind", "Meeting", "--account", acct))
	list, _ := convs["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["calendar_event_count"] != float64(4) || list[0].(map[string]any)["calendar_series_key"] == nil {
		t.Fatalf("conversations: %v", convs)
	}
	plain := ok(t, e.cmd("conversations", "--kind", "Chat", "--account", acct))
	for _, c := range plain["items"].([]any) {
		if _, has := c.(map[string]any)["calendar_event_count"]; has {
			t.Fatalf("a chat with no event has a calendar link: %v", c)
		}
	}
}
