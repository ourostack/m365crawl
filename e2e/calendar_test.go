//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
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

// outlookRoot is a profiles directory with the fixture's two Outlook profiles, Main and Second.
func outlookRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range map[string]string{"Main": "HxStore.hxd", "Second": "profile-two/HxStore.hxd"} {
		b, err := os.ReadFile(filepath.Join("..", "testdata", "outlook-fixture", filepath.FromSlash(src))) //nolint:gosec // a committed fixture
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
			t.Fatal(err)
		}
	}
	return root
}

func sourcesOf(it any) string {
	var out []string
	for _, s := range it.(map[string]any)["sources"].([]any) {
		out = append(out, s.(string))
	}
	return strings.Join(out, ",")
}

// The binary reads the two fixtures together: Outlook-only events say what they do not know, an
// explicit link merges the twins of the linked profile alone, and none undoes it.
func TestE2ECalendarOutlook(t *testing.T) {
	e := newEnv(t)
	root := outlookRoot(t)
	cal := func(extra ...string) map[string]any {
		return ok(t, e.cmd(append([]string{"calendar", "--outlook-root", root, "--max-age", "0", "--limit", "200"}, extra...)...))
	}
	mustExit(t, e.cmd("sync", "--outlook-root", root), 0)

	// Unlinked: the Outlook events are their own accounts, with their unknown fields named.
	m := cal("--from", "2023-11-20T00:00:00Z", "--to", "2023-11-26T00:00:00Z")
	if got := m["unlinked_accounts"].([]any); len(got) != 2 {
		t.Fatalf("unlinked_accounts %v", got)
	}
	if fix := m["unlinked_fix"].([]any); len(fix) != 2 || !strings.HasPrefix(fix[0].(string), "teamscrawl sync --outlook-profile Main --outlook-account ") {
		t.Fatalf("unlinked_fix %v", fix)
	}
	only := 0
	for _, it := range m["items"].([]any) {
		it := it.(map[string]any)
		if sourcesOf(it) != "outlook" {
			if len(it["sources"].([]any)) != 1 {
				t.Fatalf("an unlinked twin merged: %v", it)
			}
			continue
		}
		only++
		if it["account_id"] != "outlook/Main" || it["detail_level"] == nil || len(it["unknown_fields"].([]any)) == 0 {
			t.Fatalf("an Outlook-only item: %v", it)
		}
	}
	if only == 0 {
		t.Fatal("no Outlook-only events")
	}
	for _, it := range cal("--from", "2023-11-20T00:00:00Z", "--to", "2023-11-26T00:00:00Z", "--account", account1)["items"].([]any) {
		if strings.Contains(sourcesOf(it), "outlook") {
			t.Fatalf("--account listed an Outlook event: %v", it)
		}
	}

	// A range no source covers is a gap, and the text says why.
	far := cal("--from", "2040-01-01", "--days", "2")
	if far["coverage_gap"] != true || len(far["uncovered_days"].([]any)) != 2 {
		t.Fatalf("%v", far)
	}
	res := e.cmd("calendar", "--outlook-root", root, "--max-age", "0", "--format", "text", "--from", "2040-01-01", "--days", "2")
	mustExit(t, res, 0)
	if !strings.Contains(res.stdout, "no cached data covers 2 day(s) of this range: 2040-01-01, 2040-01-02") {
		t.Fatalf("text: %s", res.stdout)
	}

	// An unknown Teams account is a usage error and links nothing.
	res = e.cmd("sync", "--outlook-root", root, "--outlook-profile", "Main", "--outlook-account", tenant1+"/unknown")
	mustExit(t, res, 2)
	// Two profiles: say which.
	mustExit(t, e.cmd("sync", "--outlook-root", root, "--outlook-account", account1), 2)

	// Linked: the twins merge for that account alone.
	mustExit(t, e.cmd("sync", "--outlook-root", root, "--outlook-profile", "Main", "--outlook-account", account1), 0)
	linked := cal("--from", "2023-11-20T00:00:00Z", "--to", "2023-11-26T00:00:00Z", "--account", account1)
	merged := 0
	for _, it := range linked["items"].([]any) {
		if sourcesOf(it) == "teams,outlook" {
			merged++
			if it.(map[string]any)["filled_fields"] == nil {
				t.Fatalf("a merged twin names no filled field: %v", it)
			}
		}
	}
	if merged == 0 || linked["unlinked_accounts"] != nil {
		t.Fatalf("merged %d, unlinked %v", merged, linked["unlinked_accounts"])
	}
	all := cal("--from", "2023-11-20T00:00:00Z", "--to", "2032-01-01T00:00:00Z")
	if got := all["unlinked_accounts"].([]any); len(got) != 1 || got[0] != "outlook/Second" {
		t.Fatalf("only the second profile stays unlinked: %v", got)
	}

	mustExit(t, e.cmd("sync", "--outlook-root", root, "--outlook-account", "none"), 0)
	if got := cal("--from", "2023-11-20T00:00:00Z", "--to", "2023-11-26T00:00:00Z")["unlinked_accounts"].([]any); len(got) != 2 {
		t.Fatalf("after none: %v", got)
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
