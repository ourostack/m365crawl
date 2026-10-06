//go:build e2e

package e2e

import "testing"

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
