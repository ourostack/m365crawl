package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

// outlookStoreRoot is a profiles directory holding one profile, Main, whose store is the named
// fixture file.
func outlookStoreRoot(t *testing.T, file string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "outlook-fixture", file)) //nolint:gosec // G304: a committed fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	return root
}

func sourceRows(t *testing.T, e *env, args ...string) map[string]map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "calendar", "sources"}, args...)...)
	if code != 0 {
		t.Fatalf("calendar sources: exit %d: %s", code, errOut)
	}
	rows := map[string]map[string]any{}
	for _, it := range items(t, decode(t, out)) {
		rows[it["account_id"].(string)] = it
	}
	return rows
}

func num(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	f, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s is not a number in %v", key, m)
	}
	return int(f)
}

// The fixture archive reports each Teams account's covered days, detail counts, recap counts (a
// recap row with no text and no items is not content) and the zone name no IANA id exists for.
func TestCalendarSourcesOnTheFixtureArchive(t *testing.T) {
	e := calEnv(t)
	rows := sourceRows(t, e)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	r := rows[teamsAccount]
	want := map[string]int{"covered_days": 7, "events_live": 14, "events_removed": 0, "events_with_detail": 1, "events_with_attendees": 1,
		"events_with_body": 1, "events_online": 5, "recaps_total": 5, "recaps_with_content": 3, "recaps_linked": 5, "recap_action_items": 4}
	for k, v := range want {
		if got := num(t, r, k); got != v {
			t.Errorf("%s = %d, want %d", k, got, v)
		}
	}
	if r["source"] != "teams" || r["principal"] != teamsAccount || r["link"] != "none" || r["tenant_id"] != tenantA || r["user_id"] != userA {
		t.Errorf("identity %v", r)
	}
	if got := asStrings(r["unknown_time_zones"]); len(got) != 1 || got[0] != "FixtureUnknownSt" {
		t.Errorf("unknown_time_zones %v", got)
	}
	for _, k := range []string{"window_start", "window_end", "last_verified_at", "synced_at", "cache_fresh_at"} {
		if _, err := time.Parse(time.RFC3339Nano, r[k].(string)); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	for _, k := range []string{"status", "next_read_after", "deletions", "error", "unknown_layouts"} {
		if _, has := r[k]; has {
			t.Errorf("a Teams row has %s", k)
		}
	}
	if got := sourceRows(t, e, "--account", teamsAccount); len(got) != 1 || got[teamsAccount] == nil {
		t.Errorf("--account: %v", got)
	}
	code, out, _ := e.run("--max-age", "0", "--fields", "account_id,covered_days", "calendar", "sources", "--account", teamsAccount)
	if m := items(t, decode(t, out)); code != 0 || len(m) != 1 || len(m[0]) != 2 {
		t.Errorf("--fields: %d %s", code, out)
	}
	code, _, errOut := e.run("--max-age", "0", "--fields", "nope", "calendar", "sources")
	if code != 2 || !strings.Contains(errOut, "unknown --fields key") {
		t.Errorf("a bad field: %d %s", code, errOut)
	}
}

func TestCalendarSourcesOldArchiveAndNoArchive(t *testing.T) {
	e := calEnv(t)
	e.exec(`drop table calendar_recaps`)
	code, out, errOut := e.run("--max-age", "0", "calendar", "sources")
	m := decode(t, out)
	if code != 0 || m["needs_sync"] != true || !strings.Contains(out, "run m365crawl sync") || len(items(t, m)) != 0 {
		t.Errorf("an old archive: %d %s %s", code, out, errOut)
	}
	code, out, errOut = newEnv(t).run("--max-age", "0", "calendar", "sources")
	if code != 0 || decode(t, out)["needs_sync"] != true {
		t.Errorf("no archive: %d %s %s", code, out, errOut)
	}
}

// With the Outlook fixture synced, the Outlook row shows its status, events, covered days, the read
// interval and the census; a second sync inside the interval shows skipped_interval with the time
// the next read is allowed; a Teams row does not change; a link says so on the row.
func TestCalendarSourcesShowsOutlook(t *testing.T) {
	e := calEnv(t)
	teams := sourceRows(t, e)[teamsAccount]
	root := outlookStoreRoot(t, "HxStore.hxd")
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	rows := sourceRows(t, e)
	o := rows["outlook/Main"]
	if o == nil || o["source"] != "outlook" || o["status"] != "ok" || o["link"] != "none" || o["principal"] != "outlook/Main" || o["deletions"] != "inferred" {
		t.Fatalf("outlook row %v", o)
	}
	if num(t, o, "events_live") == 0 || num(t, o, "covered_days") == 0 || num(t, o, "read_interval_seconds") != 300 {
		t.Fatalf("outlook counts %v", o)
	}
	for _, k := range []string{"last_read_at", "last_attempt_at", "window_start", "blocks_invalid_ratio"} {
		if _, has := o[k]; !has {
			t.Errorf("no %s in %v", k, o)
		}
	}
	// The store holds many objects of classes that are not calendar; none of them is an unknown layout.
	if _, has := o["unknown_layouts"]; has {
		t.Errorf("a healthy store reports unknown layouts: %v", o["unknown_layouts"])
	}
	if o["unknown_time_zones"] == nil {
		t.Errorf("an Outlook row says which zone codes it could not map, even when none: %v", o)
	}
	if got := rows[teamsAccount]; num(t, got, "events_live") != num(t, teams, "events_live") || num(t, got, "covered_days") != num(t, teams, "covered_days") {
		t.Errorf("the Outlook account changed a Teams row: %v vs %v", got, teams)
	}
	// An idle store and a stalled sync differ: last_checked_at moves when the store was looked at.
	for _, k := range []string{"last_checked_at", "census_as_of"} {
		if _, has := o[k]; !has {
			t.Errorf("no %s in %v", k, o)
		}
	}
	// A failure row still holds the last good read's census, and says how old it is.
	e.exec(`insert into meta(key, value) values('outlook_failure:outlook/Main', '{"code":"outlook_store_version","message":"m","fix":"f","exit":3}')`)
	if f := sourceRows(t, e)["outlook/Main"]; f["status"] != "unsupported_version" || f["error"] == nil || f["census_as_of"] != o["census_as_of"] || f["blocks_invalid_ratio"] == nil {
		t.Errorf("a failure row: %v", f)
	}
	e.exec(`delete from meta where key='outlook_failure:outlook/Main'`)
	// A layout of the event class this build does not know is listed with its class and tag in hex.
	e.exec(`update meta set value='{"at":"2026-10-06T00:00:00.000Z","unknown_layouts":[{"class":107,"tag":1110,"count":3}],"blocks_found":10,"blocks_invalid":1}' where key='outlook_read:outlook/Main'`)
	if l, _ := sourceRows(t, e)["outlook/Main"]["unknown_layouts"].([]any); len(l) != 1 || l[0].(map[string]any)["class"] != "0x6b" || l[0].(map[string]any)["tag"] != "0x456" || l[0].(map[string]any)["count"] != float64(3) {
		t.Errorf("unknown_layouts %v", l)
	}
	// A second sync inside the interval reads nothing and says so.
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	o = sourceRows(t, e)["outlook/Main"]
	if o["status"] != "skipped_interval" {
		t.Fatalf("status %v", o["status"])
	}
	next, err := time.Parse(time.RFC3339Nano, o["next_read_after"].(string))
	if err != nil || !next.After(time.Now()) {
		t.Fatalf("next_read_after %v %v", o["next_read_after"], err)
	}
	// An explicit link joins the profile to the Teams account.
	e.exec(`insert into calendar_account_links(source,account_id,principal_id,method,linked_at) values('outlook','outlook/Main','` + teamsAccount + `','config','2026-10-06T00:00:00.000Z')`)
	rows = sourceRows(t, e)
	if rows["outlook/Main"]["link"] != "config" || rows["outlook/Main"]["principal"] != teamsAccount || num(t, rows[teamsAccount], "events_live") != num(t, teams, "events_live") {
		t.Fatalf("linked rows %v", rows)
	}
	if got := sourceRows(t, e, "--account", teamsAccount); len(got) != 2 || got["outlook/Main"] == nil {
		t.Fatalf("--account must keep the linked profile: %v", got)
	}
}

// A store this build cannot read is reported on its row with the code and the fix, and its status
// says why Outlook is not being read.
func TestCalendarSourcesShowsEachGuardCode(t *testing.T) {
	for _, c := range []struct{ file, status, code string }{
		{"store-version-j.hxd", "unsupported_version", "outlook_store_version"},
		{"store-new-tag-mixed.hxd", "unsupported_layout", "outlook_layout_unsupported"},
		{"store-not-hxstore.hxd", "unreadable", "outlook_store_unrecognized"},
	} {
		t.Run(c.status, func(t *testing.T) {
			e := calEnv(t)
			root := outlookStoreRoot(t, c.file)
			if code, _, _ := e.run("--outlook-root", root, "sync"); code != 1 {
				t.Fatalf("a failed Outlook source makes the sync partial: exit %d", code)
			}
			o := sourceRows(t, e)["outlook/Main"]
			errObj, _ := o["error"].(map[string]any)
			if o["status"] != c.status || errObj["code"] != c.code || errObj["message"] == "" || errObj["fix"] == "" {
				t.Fatalf("row %v", o)
			}
			if num(t, o, "events_live") != 0 {
				t.Errorf("a refused store applied events: %v", o)
			}
		})
	}
}

// The link made by the real flag shows on the Outlook row, which then belongs to the Teams principal,
// and the Teams row keeps its own counts.
func TestCalendarSourcesShowsTheLinkMadeByTheFlag(t *testing.T) {
	e := calEnv(t)
	teams := sourceRows(t, e)[teamsAccount]
	root := outlookStoreRoot(t, "HxStore.hxd")
	if code, _, stderr := e.run("--outlook-root", root, "--outlook-account", teamsAccount, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	rows := sourceRows(t, e)
	if o := rows["outlook/Main"]; o["link"] != "config" || o["principal"] != teamsAccount {
		t.Fatalf("linked row %v", o)
	}
	if rows[teamsAccount]["link"] != "none" || num(t, rows[teamsAccount], "events_live") != num(t, teams, "events_live") {
		t.Fatalf("the Teams row changed: %v", rows[teamsAccount])
	}
	if code, _, stderr := e.run("--outlook-root", root, "--outlook-account", "none", "sync"); code != 0 {
		t.Fatalf("unlink exit %d: %s", code, stderr)
	}
	if o := sourceRows(t, e)["outlook/Main"]; o["link"] != "none" || o["principal"] != "outlook/Main" {
		t.Fatalf("after none: %v", o)
	}
}

func TestCalendarSourcesTextGoldens(t *testing.T) {
	e := calEnv(t)
	if code, _, stderr := e.run("--outlook-root", outlookStoreRoot(t, "HxStore.hxd"), "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"calendar_sources", []string{"calendar", "sources"}},
		{"calendar_sources_account", []string{"calendar", "sources", "--account", teamsAccount}},
	} {
		for _, color := range []bool{false, true} {
			t.Setenv("CLICOLOR_FORCE", "")
			suffix := "plain"
			if color {
				suffix = "color"
				t.Setenv("CLICOLOR_FORCE", "1")
			}
			code, out, errOut := e.run(append([]string{"--format", "text", "--max-age", "0"}, c.args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
			}
			checkGolden(t, c.name+"."+suffix, e.scrub(out))
		}
	}
}

// doctor shows an Outlook source that is on and whose last read failed: a warning with the fix.
func TestDoctorOutlookGolden(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the doctor golden has a Windows variant for the Teams checks only")
	}
	e := calEnv(t)
	root := outlookStoreRoot(t, "HxStore.hxd")
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	e.exec(`insert into meta(key, value) values('outlook_failure:outlook/Main', '{"code":"outlook_layout_unsupported","message":"the Outlook store cannot be read by this version of m365crawl: class 0x6b tag 0x456 x3, known 0x455","fix":"Update m365crawl: this version reads Outlook store version i, event layout 0x6b/0x455. Nothing of Outlook was applied.","exit":3}')`)
	for _, color := range []bool{false, true} {
		t.Setenv("CLICOLOR_FORCE", "")
		suffix := "plain"
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.run("--format", "text", "--outlook-root", root, "doctor")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "doctor_outlook."+suffix, e.scrub(out))
	}
}

func TestShortAccountAndWindow(t *testing.T) {
	for in, want := range map[string]string{"outlook/Main": "outlook/Main", "plain": "plain", "t/u": "t/u",
		"00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1": "0000…0001/0000…00a1"} {
		if got := shortAccount(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if sourceWindow(calendarSourceItem{}) != "-" {
		t.Error("an empty window")
	}
}

// doctor's outlook_store check: off by default; on, it names the profiles, the header verdict and the
// last read, and warns, never fails, for missing profiles, an unknown store version and a failed read.
func TestDoctorOutlookStoreCheck(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	rt := &runtime{ctx: context.Background(), now: func() time.Time { return now }}
	if c := rt.outlookStoreCheck(nil); !c.OK || c.Warn || !strings.Contains(c.Detail, "Outlook source is off") || !strings.Contains(c.Detail, "--outlook-root none") {
		t.Fatalf("off: %+v", c)
	}
	rt.outlookOn = true

	t.Run("no default root", func(t *testing.T) {
		old := outlookDefaultRoot
		t.Cleanup(func() { outlookDefaultRoot = old })
		outlookDefaultRoot = func() (string, error) { return "", errors.New("not here") }
		if c := rt.outlookStoreCheck(nil); c.Warn || !c.OK || !strings.Contains(c.Detail, "not here") || c.Fix != "" {
			t.Fatalf("a machine that has no Outlook directory is not a problem: %+v", c)
		}
	})
	t.Run("on by default", func(t *testing.T) {
		rt := *rt
		rt.outlookDefault = true
		absent := filepath.Join(t.TempDir(), "absent")
		old := outlookDefaultRoot
		t.Cleanup(func() { outlookDefaultRoot = old })
		outlookDefaultRoot = func() (string, error) { return absent, nil }
		if c := rt.outlookStoreCheck(nil); c.Warn || !c.OK || !strings.Contains(c.Detail, "not installed") {
			t.Fatalf("no Outlook: %+v", c)
		}
		outlookDefaultRoot = func() (string, error) { return t.TempDir(), nil }
		if c := rt.outlookStoreCheck(nil); c.Warn || !c.OK || !strings.Contains(c.Detail, "no profile") {
			t.Fatalf("no profile: %+v", c)
		}
		outlookDefaultRoot = func() (string, error) { return outlookStoreRoot(t, "HxStore.hxd"), nil }
		if c := rt.outlookStoreCheck(nil); c.Warn || !c.OK || !strings.Contains(c.Detail, "profile Main: store version readable; not read yet") {
			t.Fatalf("a normal state is a plain pass: %+v", c)
		}
		// Named by the operator, the same missing directory is a warning.
		rt.outlookDefault, rt.outlookRoot = false, absent
		if c := rt.outlookStoreCheck(nil); !c.Warn {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("missing root, empty root", func(t *testing.T) {
		for _, root := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
			rt := *rt
			rt.outlookRoot = root
			if c := rt.outlookStoreCheck(nil); !c.Warn || !c.OK || !strings.Contains(c.Detail, "no Outlook profile found under "+root) || !strings.Contains(c.Fix, "<root>/<profile>/HxStore.hxd") {
				t.Fatalf("%+v", c)
			}
		}
	})
	t.Run("access denied", func(t *testing.T) {
		old := outlookDiscover
		t.Cleanup(func() { outlookDiscover = old })
		outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
			return nil, nil, nil, &errs.Coded{Code: errs.CodeNoFullDiskAccess, Message: "denied", Fix: "grant it"}
		}
		rt := *rt
		rt.outlookRoot = "x"
		if c := rt.outlookStoreCheck(nil); !c.Warn || c.Detail != "denied" || c.Fix != "grant it" {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("profiles", func(t *testing.T) {
		rt := *rt
		rt.outlookRoot = outlookStoreRoot(t, "HxStore.hxd")
		if c := rt.outlookStoreCheck(nil); c.Warn || !strings.Contains(c.Detail, "profile Main: store version readable; not read yet") {
			t.Fatalf("unread: %+v", c)
		}
		bad := outlookStoreRoot(t, "store-version-j.hxd")
		rt.outlookRoot = bad
		if c := rt.outlookStoreCheck(nil); !c.Warn || !c.OK || !strings.Contains(c.Detail, `store is version 'j'`) || !strings.Contains(c.Detail, "unsupported_version") || !strings.Contains(c.Fix, "Update m365crawl") {
			t.Fatalf("version: %+v", c)
		}
		rt.outlookRoot = outlookStoreRoot(t, "store-short.hxd")
		if c := rt.outlookStoreCheck(nil); !c.Warn || !strings.Contains(c.Detail, "unreadable") || !strings.Contains(c.Fix, "Full Disk Access") {
			t.Fatalf("short: %+v", c)
		}
	})
	t.Run("skipped profile", func(t *testing.T) {
		old := outlookDiscover
		t.Cleanup(func() { outlookDiscover = old })
		outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
			return nil, nil, []outlookdesktop.SkippedProfile{{Name: "Two", Dir: "/p/Two", Reason: "no_full_disk_access"}}, nil
		}
		rt := *rt
		rt.outlookRoot = "x"
		if c := rt.outlookStoreCheck(nil); !c.Warn || !strings.Contains(c.Detail, "profile Two cannot be examined (no_full_disk_access)") || !strings.Contains(c.Fix, "/p/Two") {
			t.Fatalf("%+v", c)
		}
	})
}

// With an archive, the check adds what the last sync recorded: when it last read, or the failure.
func TestDoctorOutlookStoreCheckReadsTheArchive(t *testing.T) {
	e := calEnv(t)
	root := outlookStoreRoot(t, "HxStore.hxd")
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	doctor := func() check {
		t.Helper()
		_, out, _ := e.run("--outlook-root", root, "doctor")
		for _, c := range decodeChecks(t, out) {
			if c.Name == "outlook_store" {
				return c
			}
		}
		t.Fatalf("no outlook_store check in %s", out)
		return check{}
	}
	if c := doctor(); !c.OK || c.Warn || !strings.Contains(c.Detail, "store version readable; last read ") || !strings.Contains(c.Detail, "ago, ok") {
		t.Fatalf("after a read: %+v", c)
	}
	e.exec(`insert into meta(key, value) values('outlook_failure:outlook/Main', '{"code":"outlook_layout_unsupported","message":"class 0x6b tag 0x456","fix":"","exit":3}')`)
	if c := doctor(); !c.OK || !c.Warn || !strings.Contains(c.Detail, "the last read failed (unsupported_layout): outlook_layout_unsupported: class 0x6b tag 0x456") || !strings.Contains(c.Fix, "m365crawl sync") {
		t.Fatalf("after a failure: %+v", c)
	}
	e.exec(`delete from meta where key like 'outlook_failure:%'`)
	e.exec(`insert into calendar_account_links(source, account_id, principal_id, method, linked_at) values('outlook', 'outlook/Main', 'x/y', 'address', '2026-10-06T00:00:00.000Z')`)
	if c := doctor(); !strings.Contains(c.Detail, "linked to a Teams account (address)") {
		t.Fatalf("a linked profile: %+v", c)
	}
	// doctor never fails because of Outlook.
	if code, _, _ := e.run("--outlook-root", root, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
}

func TestDoctorOutlookStateReadFailureWarns(t *testing.T) {
	rt := &runtime{ctx: context.Background(), now: time.Now, outlookOn: true, outlookRoot: outlookStoreRoot(t, "HxStore.hxd")}
	old := readCalendarSources
	t.Cleanup(func() { readCalendarSources = old })
	readCalendarSources = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
		return store.CalendarSources{}, errors.New("disk on fire")
	}
	if c := rt.outlookStoreCheck(&store.Store{}); !c.Warn || !strings.Contains(c.Detail, "cannot read the Outlook state: disk on fire") || !strings.Contains(c.Fix, "m365crawl sync") {
		t.Fatalf("%+v", c)
	}
}

func decodeChecks(t *testing.T, out string) []check {
	t.Helper()
	var res doctorResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return res.Checks
}
