package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/calendar"
)

const teamsAccount = tenantA + "/" + userA

// outlookProfiles is a profiles directory holding the named fixture profiles: Main is the fixture
// store, Second the second profile's.
// outlookRoots remembers the profiles directory of each test that built one.
var outlookRoots = map[string]string{}

func outlookRootOf(t *testing.T, _ func(args ...string) (int, string, string)) string {
	return outlookRoots[t.Name()]
}

func outlookProfiles(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		src := "../../testdata/outlook-fixture/HxStore.hxd"
		if name != "Main" {
			src = "../../testdata/outlook-fixture/profile-two/HxStore.hxd"
		}
		b, err := os.ReadFile(src) //nolint:gosec // a committed fixture
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
			t.Fatal(err)
		}
	}
	return root
}

// syncedWithOutlook is an archive that holds the Teams fixture and the named Outlook profiles.
// The returned run adds --outlook-root to every call.
func syncedWithOutlook(t *testing.T, names ...string) (*env, func(args ...string) (int, string, string)) {
	t.Helper()
	e := textEnv(t)
	root := outlookProfiles(t, names...)
	run := func(args ...string) (int, string, string) {
		return e.run(append([]string{"--outlook-root", root}, args...)...)
	}
	outlookRoots[t.Name()] = root
	if code, _, stderr := run("sync"); code != 0 {
		t.Fatalf("sync exit %d: %s", code, stderr)
	}
	return e, run
}

func agendaVia(t *testing.T, run func(args ...string) (int, string, string), args ...string) map[string]any {
	t.Helper()
	code, out, errOut := run(append([]string{"--max-age", "0", "calendar", "--limit", "200"}, args...)...)
	if code != 0 {
		t.Fatalf("calendar %v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func eventVia(t *testing.T, run func(args ...string) (int, string, string), ref string) map[string]any {
	t.Helper()
	code, out, errOut := run("--max-age", "0", "calendar", "event", ref)
	if code != 0 {
		t.Fatalf("calendar event %s: exit %d: %s", ref, code, errOut)
	}
	return decode(t, out)
}

func sourcesOf(it map[string]any) string { return strings.Join(asStrings(it["sources"]), ",") }

func subjectsBySources(t *testing.T, m map[string]any, sources, from string) []string {
	t.Helper()
	var out []string
	for _, it := range items(t, m) {
		if sourcesOf(it) == sources && it["start"].(string) >= from {
			out = append(out, it["subject"].(string))
		}
	}
	return out
}

func usageBody(t *testing.T, stderr string) map[string]any {
	t.Helper()
	e, _ := decode(t, stderr)["error"].(map[string]any)
	if e == nil {
		t.Fatalf("no error body: %s", stderr)
	}
	return e
}

// An Outlook-only event says what it does not know and how complete the archive is for its days,
// and a Teams account filter leaves it out: the filter names a Teams account.
func TestCalendarOutlookOnlyEvents(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	m := agendaVia(t, run, "--from", "2023-11-20", "--to", "2023-11-26")
	for _, it := range items(t, m) {
		if sourcesOf(it) != "outlook" {
			continue
		}
		unknown := asStrings(it["unknown_fields"])
		if it["account_id"] != "outlook/Main" || len(unknown) == 0 || it["detail_level"] == nil {
			t.Fatalf("an Outlook-only item must name its account and what it does not know: %v", it)
		}
		if got := it["detail_level"] == "basic"; got != slices.Contains(unknown, "attendees") && got != slices.Contains(unknown, "body") {
			t.Fatalf("detail_level %v with unknown %v", it["detail_level"], unknown)
		}
	}
	if got := asStrings(m["unlinked_accounts"]); len(got) != 1 || got[0] != "outlook/Main" {
		t.Fatalf("unlinked_accounts %v", got)
	}
	// The envelope names the exact command to link the profile. Two Teams accounts are in the
	// archive, so the Teams account is a placeholder.
	if got := asStrings(m["unlinked_fix"]); len(got) != 1 || got[0] != "teamscrawl sync --outlook-profile Main --outlook-account <tenantId>/<userId>" {
		t.Fatalf("unlinked_fix %v", got)
	}

	// The Teams account filter omits them, and with them the unlinked notice.
	teams := agendaVia(t, run, "--from", "2023-11-20", "--to", "2023-11-26", "--account", teamsAccount)
	for _, it := range items(t, teams) {
		if strings.Contains(sourcesOf(it), "outlook") {
			t.Fatalf("--account listed an unlinked Outlook event: %v", it)
		}
	}
	if teams["unlinked_accounts"] != nil || teams["unlinked_fix"] != nil {
		t.Fatalf("%v %v", teams["unlinked_accounts"], teams["unlinked_fix"])
	}

	// A range no source covers is a gap, and the text says which days and why.
	far := agendaVia(t, run, "--from", "2040-01-01", "--days", "3")
	if far["coverage_gap"] != true || len(asStrings(far["uncovered_days"])) != 3 || far["count"].(float64) != 0 {
		t.Fatalf("an uncovered range: %v", far)
	}
	code, out, errOut := run("--format", "text", "--max-age", "0", "calendar", "--from", "2040-01-01", "--days", "3")
	if code != 0 || !strings.Contains(out, "no cached data covers 3 day(s) of this range: 2040-01-01, 2040-01-02, 2040-01-03") {
		t.Fatalf("text: %d %s %s", code, out, errOut)
	}
}

func TestCalendarOutlookGoldens(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	m := agendaVia(t, run, "--from", "2031-03-05", "--days", "1")
	item := itemBySubject(t, m, "Fixture all-day event")
	if item["detail_level"] != "basic" || sourcesOf(item) != "outlook" {
		t.Fatalf("%v", item)
	}
	b, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "calendar_outlook_item.json", string(b)+"\n")

	for _, color := range []bool{false, true} {
		t.Setenv("CLICOLOR_FORCE", "")
		suffix := "plain"
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		for name, args := range map[string][]string{
			"calendar_outlook":       {"calendar", "--from", "2023-11-21", "--days", "2", "--account", "outlook/Main"},
			"calendar_outlook_event": {"calendar", "event", item["event_id"].(string)},
		} {
			code, out, errOut := run(append([]string{"--format", "text", "--max-age", "0"}, args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", name, code, errOut)
			}
			checkGolden(t, name+"."+suffix, e.scrub(out))
		}
		code, out, errOut := run("--format", "text", "--max-age", "0", "calendar", "--from", "2023-11-21", "--days", "2")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "calendar_outlook_unlinked."+suffix, e.scrub(out))
	}
}

func TestOutlookOnlyNote(t *testing.T) {
	for _, c := range []struct {
		sources, unknown []string
		want             string
	}{
		{[]string{"teams"}, []string{"attendees"}, ""},
		{[]string{"teams", "outlook"}, nil, ""},
		{[]string{"outlook"}, []string{"recurrence"}, "outlook is the only source of this event"},
		{[]string{"outlook"}, []string{"attendees", "short_join_url", "join_url", "body"}, "outlook is the only source of this event; not known: attendees, join link, body"},
		{[]string{"outlook"}, []string{"join_url"}, "outlook is the only source of this event; not known: join link"},
	} {
		if got := outlookOnlyNote(c.sources, c.unknown); got != c.want {
			t.Errorf("%v %v: %q", c.sources, c.unknown, got)
		}
	}
}

func TestLinkFixes(t *testing.T) {
	if linkFixes(nil, nil) != nil {
		t.Fatal("no unlinked account, no fix")
	}
	// With one Teams account in scope the hint names it; with two it keeps the placeholder.
	one := []calendar.AccountCoverage{{AccountID: teamsAccount}, {AccountID: "outlook/Main"}}
	if got := linkFixes([]string{"outlook/Main"}, one); got[0] != "teamscrawl sync --outlook-profile Main --outlook-account "+teamsAccount {
		t.Fatal(got)
	}
	two := append(slices.Clone(one), calendar.AccountCoverage{AccountID: tenantA + "/other"})
	if got := linkFixes([]string{"outlook/My Mail"}, two); got[0] != "teamscrawl sync --outlook-profile 'My Mail' --outlook-account <tenantId>/<userId>" {
		t.Fatal(got)
	}
	if got := shellWord("Main"); got != "Main" {
		t.Fatal(got)
	}
	for in, want := range map[string]string{"": "''", "My Profile": "'My Profile'", "it's": `'it'\''s'`} {
		if got := shellWord(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A link merges the twins of the two fixtures, names the Teams account's tenant and user on the
// events only Outlook holds, keeps their old ids resolving, and can be undone.
func TestOutlookLinkMergesAgenda(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	const window = "2023-11-20"
	before := agendaVia(t, run, "--from", window, "--to", "2023-11-26")
	standups := 0
	for _, it := range items(t, before) {
		if it["subject"] == "Fixture standup" && it["start"] == "2023-11-22T18:00:00Z" {
			standups++
		}
	}
	if standups != 3 { // two Teams accounts and the unlinked Outlook profile
		t.Fatalf("%d standups before the link", standups)
	}
	old := agendaVia(t, run, "--from", "2031-03-05", "--days", "1")
	oldID := itemBySubject(t, old, "Fixture all-day event")["event_id"].(string)
	if oldID == "" {
		t.Fatal("no id")
	}
	oldTwinID := itemBySubject(t, before, "Fixture plain event")["event_id"].(string)

	// A range only Outlook covers: the person's gap is the days neither source covers.
	const far = "2031-03-05"
	uncovered := func(account string) []string {
		return asStrings(agendaVia(t, run, "--from", far, "--days", "7", "--account", account)["uncovered_days"])
	}
	teamsWindow := asStrings(agendaVia(t, run, "--from", window, "--to", "2023-11-26", "--account", teamsAccount)["uncovered_days"])
	teamsOnly, outlookOnly := uncovered(teamsAccount), uncovered("outlook/Main")
	if len(teamsOnly) != 7 || len(outlookOnly) == 0 || len(outlookOnly) >= 7 {
		t.Fatalf("teams %v, outlook %v", teamsOnly, outlookOnly)
	}

	code, _, errOut := run("sync", "--outlook-account", teamsAccount)
	if code != 0 {
		t.Fatalf("link: exit %d: %s", code, errOut)
	}
	if got := uncovered(teamsAccount); !slices.Equal(got, outlookOnly) {
		t.Fatalf("after the link the gap is %v, want Outlook's %v", got, outlookOnly)
	}
	m := agendaVia(t, run, "--from", window, "--to", "2023-11-26", "--account", teamsAccount)
	if m["unlinked_accounts"] != nil || m["unlinked_fix"] != nil {
		t.Fatalf("a linked profile is not unlinked: %v", m["unlinked_accounts"])
	}
	// Linked twins are one item with both sources, and the fields only one source had are said.
	merged := 0
	for _, it := range items(t, m) {
		if sourcesOf(it) != "teams,outlook" {
			continue
		}
		merged++
		if it["account_id"] != teamsAccount || it["filled_fields"] == nil {
			t.Fatalf("a merged twin: %v", it)
		}
	}
	if merged != 4 {
		t.Fatalf("%d merged twins, want the fixtures' 4 in the window", merged)
	}
	standups = 0
	for _, it := range items(t, m) {
		if it["subject"] == "Fixture standup" && it["start"] == "2023-11-22T18:00:00Z" {
			standups++
		}
	}
	if standups != 1 {
		t.Fatalf("%d standups after the link", standups)
	}
	if got := asStrings(m["uncovered_days"]); m["coverage_gap"] != true || len(got) != 1 || !slices.Equal(got, teamsWindow) {
		t.Fatalf("coverage: %v %v, want the Teams account's own %v", m["coverage_gap"], got, teamsWindow)
	}
	if got := subjectsBySources(t, m, "outlook", window); len(got) != 0 {
		t.Fatalf("an Outlook-only event in a window of twins: %v", got)
	}

	// An event only Outlook holds now belongs to the Teams account, and its old id still opens it.
	after := agendaVia(t, run, "--from", "2031-03-05", "--days", "1", "--account", teamsAccount)
	moved := itemBySubject(t, after, "Fixture all-day event")
	if sourcesOf(moved) != "outlook" || moved["account_id"] != teamsAccount || moved["event_id"] == oldID {
		t.Fatalf("%v", moved)
	}
	for _, id := range []string{oldID, oldTwinID} {
		ev := eventVia(t, run, id)
		if ev["account_id"] != teamsAccount || ev["tenant_id"] != tenantA || ev["user_id"] != userA {
			t.Fatalf("%s: %v", id, ev)
		}
	}
	if eventVia(t, run, oldID)["event_id"] != moved["event_id"] {
		t.Fatal("the old id opens another event")
	}

	// The link stays without the flag, and is undone with none; undoing it twice is not an error.
	if code, _, errOut = run("sync"); code != 0 {
		t.Fatal(errOut)
	}
	if again := agendaVia(t, run, "--from", window, "--to", "2023-11-26"); again["unlinked_accounts"] != nil {
		t.Fatalf("the link was lost: %v", again["unlinked_accounts"])
	}
	for range 2 {
		if code, _, errOut = run("sync", "--outlook-account", "none"); code != 0 {
			t.Fatalf("unlink: exit %d: %s", code, errOut)
		}
	}
	undone := agendaVia(t, run, "--from", window, "--to", "2023-11-26")
	if got := asStrings(undone["unlinked_accounts"]); len(got) != 1 {
		t.Fatalf("after none: %v", got)
	}
	for _, it := range items(t, undone) {
		if len(asStrings(it["sources"])) > 1 {
			t.Fatalf("an unlinked twin stayed merged: %v", it)
		}
	}
}

// A link that cannot work is a usage error before anything is merged, and says how to fix it.
func TestOutlookLinkUsageErrors(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main", "Second")
	for _, c := range []struct {
		name string
		args []string
		fix  string
	}{
		{"unknown Teams account", []string{"sync", "--outlook-profile", "Main", "--outlook-account", "00000000-0000-4000-8000-0000000000ff/00000000-0000-4000-8000-0000000000ff"}, "Name a Teams account"},
		{"not an account", []string{"sync", "--outlook-profile", "Main", "--outlook-account", "teams-me"}, "whoami"},
		{"which profile", []string{"sync", "--outlook-account", teamsAccount}, "one of: Main, Second"},
		{"unknown profile", []string{"sync", "--outlook-profile", "Nope", "--outlook-account", teamsAccount}, "one of: Main, Second"},
		{"profile alone", []string{"sync", "--outlook-profile", "Main"}, ""},
		{"with a Teams filter", []string{"--account", teamsAccount, "sync", "--outlook-profile", "Main", "--outlook-account", teamsAccount}, "Drop --account"},
		{"on a read", []string{"--max-age", "0", "calendar", "--outlook-profile", "Main", "--outlook-account", "teams-me"}, "whoami"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := run(c.args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2: %s %s", code, stdout, stderr)
			}
			body := usageBody(t, stderr)
			if body["code"] != "usage" || !strings.Contains(body["fix"].(string), c.fix) {
				t.Fatalf("%v", body)
			}
		})
	}
	// A refused link still reports the sync that ran before it.
	code, stdout, _ := run("sync", "--outlook-account", teamsAccount)
	if code != 2 || decode(t, stdout)["status"] == nil {
		t.Fatalf("the report of a sync whose link was refused: %d %s", code, stdout)
	}
	// Nothing was linked by any of them.
	if m := agendaVia(t, run, "--from", "2023-11-20", "--days", "3"); len(asStrings(m["unlinked_accounts"])) != 2 {
		t.Fatalf("%v", m["unlinked_accounts"])
	}
}

func TestOutlookLinkNeedsOutlookOn(t *testing.T) {
	e := textEnv(t)
	for _, args := range [][]string{
		{"sync", "--outlook-account", teamsAccount},
		{"sync", "--outlook-account", teamsAccount, "--outlook-root", "none"},
	} {
		code, _, stderr := e.run(args...)
		if code != 2 || !strings.Contains(usageBody(t, stderr)["fix"].(string), "--outlook-root DIR") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
	// TEAMSCRAWL_OUTLOOK=1 is ignored beside a Teams root, so the link is refused there too.
	t.Setenv("TEAMSCRAWL_OUTLOOK", "1")
	if code, _, _ := e.run("sync", "--outlook-account", teamsAccount); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// Two profiles under one root: only the one that is linked merges. The other stays its own
// principal, is named as unlinked, and cannot be given the same Teams account.
func TestTwoProfilesLinkOnlyOne(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main", "Second")
	if code, _, errOut := run("sync", "--outlook-profile", "Main", "--outlook-account", teamsAccount); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m := agendaVia(t, run, "--from", "2023-11-20", "--to", "2032-01-01")
	if got := asStrings(m["unlinked_accounts"]); len(got) != 1 || got[0] != "outlook/Second" {
		t.Fatalf("unlinked_accounts %v", got)
	}
	if got := asStrings(m["unlinked_fix"]); len(got) != 1 || !strings.HasPrefix(got[0], "teamscrawl sync --outlook-profile Second --outlook-account ") {
		t.Fatalf("unlinked_fix %v", got)
	}
	var second []string
	for _, it := range items(t, m) {
		if it["account_id"] == "outlook/Second" {
			second = append(second, sourcesOf(it))
		}
	}
	if len(second) != 2 || second[0] != "outlook" {
		t.Fatalf("the second profile's events: %v", second)
	}
	mine := agendaVia(t, run, "--from", "2023-11-20", "--to", "2032-01-01", "--account", teamsAccount)
	for _, it := range items(t, mine) {
		if it["account_id"] == "outlook/Second" || it["subject"] == "Fixture second profile event" {
			t.Fatalf("the unlinked profile reached the account: %v", it)
		}
	}
	code, _, stderr := run("sync", "--outlook-profile", "Second", "--outlook-account", teamsAccount)
	if code != 2 || !strings.Contains(usageBody(t, stderr)["message"].(string), "already has the outlook account outlook/Main") {
		t.Fatalf("%d %s", code, stderr)
	}
	// "none" with no profile named ends every link.
	if code, _, stderr = run("sync", "--outlook-account", "none"); code != 0 {
		t.Fatal(stderr)
	}
	if got := agendaVia(t, run, "--from", "2023-11-20", "--days", "1")["unlinked_accounts"]; len(asStrings(got)) != 2 {
		t.Fatalf("%v", got)
	}
}

// Linking from a read command applies on its implicit sync even when the archive is fresh, and a
// link already in place does not ask for another sync.
func TestOutlookLinkOnARead(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	link := []string{"--outlook-account", teamsAccount}
	code, out, errOut := run(append(link, "--max-age", "1h", "calendar", "--from", "2023-11-20", "--days", "1", "--account", teamsAccount)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if synced, _ := decode(t, out)["synced"].(map[string]any); synced == nil {
		t.Fatalf("a read with a link pending did not sync: %s", out)
	}
	_, out, _ = run(append(link, "--max-age", "1h", "calendar", "--from", "2023-11-20", "--days", "1", "--account", teamsAccount)...)
	if synced, _ := decode(t, out)["synced"].(map[string]any); synced != nil {
		t.Fatalf("a link in place synced again: %s", out)
	}
	// A refused link on a read is a usage error, not a warning on a result.
	code, _, errOut = run("--max-age", "1h", "--outlook-account", "00000000-0000-4000-8000-0000000000ff/00000000-0000-4000-8000-0000000000ff", "--outlook-profile", "Main", "calendar")
	if code != 2 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	// With the profile named, none is judged for that profile.
	if code, _, errOut = run("--max-age", "1h", "--outlook-account", "none", "--outlook-profile", "Main", "calendar", "--days", "1"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if code, _, errOut = run("--max-age", "1h", "--outlook-account", "none", "calendar", "--days", "1"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	_ = e
}

// The first command on a machine may carry the link: the sync it runs creates the Teams account.
func TestOutlookLinkOnTheFirstRun(t *testing.T) {
	e := textEnv(t)
	root := outlookProfiles(t, "Main")
	code, out, errOut := e.run("--outlook-root", root, "--outlook-account", teamsAccount, "calendar", "--from", "2023-11-20", "--days", "1", "--account", teamsAccount)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m := decode(t, out)
	if m["unlinked_accounts"] != nil || len(items(t, m)) == 0 {
		t.Fatalf("%v", m)
	}
}

// The Teams twin that Teams marked gone is hidden after the link, unless the Outlook copy was
// edited after the removal.
func TestOutlookLinkTeamsRemovedTwinHidden(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	if code, _, errOut := run("sync", "--outlook-account", teamsAccount); code != 0 {
		t.Fatal(errOut)
	}
	window := []string{"--from", "2023-11-20", "--to", "2023-11-26", "--account", teamsAccount}
	const standup = "Fixture standup"
	has := func(m map[string]any, start string) map[string]any {
		for _, it := range items(t, m) {
			if it["subject"] == standup && it["start"] == start {
				return it
			}
		}
		return nil
	}
	const start = "2023-11-22T18:00:00Z"
	if has(agendaVia(t, run, window...), start) == nil {
		t.Fatal("the twin is not listed before the removal")
	}
	remove := func(at string) {
		e.exec(`update calendar_source_events set removed_at='` + at + `' where source='teams' and account_id='` + teamsAccount + `' and ical_uid like '%07e70b16%' and start_at like '2023-11-22T18:00:00%'`)
	}
	// Outlook's copy was last edited in 2031: a removal before that does not hide it.
	remove("2030-01-01T00:00:00.000Z")
	if has(agendaVia(t, run, window...), start) == nil {
		t.Fatal("a removal older than Outlook's copy hid the event")
	}
	remove("2040-01-01T00:00:00.000Z")
	if it := has(agendaVia(t, run, window...), start); it != nil {
		t.Fatalf("a removed Teams twin still shows: %v", it)
	}
	it := has(agendaVia(t, run, append(window, "--include-removed")...), start)
	if it == nil || it["removed"] != true || strings.Join(asStrings(it["removed_by"]), ",") != "teams" {
		t.Fatalf("--include-removed: %v", it)
	}
}

// An id printed while the profile was linked still opens its event after the link ends.
func TestOutlookIDsSurviveAnUnlink(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	if code, _, errOut := run("sync", "--outlook-account", teamsAccount); code != 0 {
		t.Fatal(errOut)
	}
	linked := agendaVia(t, run, "--from", "2031-03-05", "--days", "1", "--account", teamsAccount)
	linkedID := itemBySubject(t, linked, "Fixture all-day event")["event_id"].(string)
	if code, _, errOut := run("sync", "--outlook-account", "none"); code != 0 {
		t.Fatal(errOut)
	}
	ev := eventVia(t, run, linkedID)
	if ev["account_id"] != "outlook/Main" || ev["subject"] != "Fixture all-day event" || ev["event_id"] == linkedID {
		t.Fatalf("%v", ev)
	}
	// A twin's id from the linked time is still the Teams event's own id.
	twin := itemBySubject(t, agendaVia(t, run, "--from", "2023-11-20", "--days", "1", "--account", teamsAccount), "Fixture planning review")
	if got := eventVia(t, run, twin["event_id"].(string)); got["account_id"] != teamsAccount {
		t.Fatalf("%v", got)
	}
}

// A link whose profile directory is gone is ended by name, and the reads after that do not sync
// again for it.
func TestOutlookUnlinkOfAVanishedProfile(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main", "Second")
	if code, _, errOut := run("sync", "--outlook-profile", "Main", "--outlook-account", teamsAccount); code != 0 {
		t.Fatal(errOut)
	}
	// run() fixes the root, so the profile is removed through the same directory.
	root := outlookRootOf(t, run)
	if err := os.RemoveAll(filepath.Join(root, "Main")); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	args := []string{"--outlook-account", "none", "--outlook-profile", "Main"}
	if code, _, errOut := run(append([]string{"--max-age", "1h"}, append(args, "calendar", "--days", "1")...)...); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	_, out, _ := run(append([]string{"--max-age", "1h"}, append(args, "calendar", "--days", "1")...)...)
	if decode(t, out)["synced"] != nil {
		t.Fatalf("a read synced again for a link that is gone: %s", out)
	}
	// Main's events stay in the archive, so it is listed again as an unlinked account.
	m := agendaVia(t, run, "--from", "2023-11-20", "--days", "1")
	if got := asStrings(m["unlinked_accounts"]); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	m = agendaVia(t, run, "--from", "2023-11-20", "--days", "1", "--account", teamsAccount)
	if m["unlinked_accounts"] != nil {
		t.Fatalf("%v", m["unlinked_accounts"])
	}
}

// --max-age 0 turns the implicit sync off, so a link that still has to be applied cannot be, and
// the error says how to apply it.
func TestOutlookLinkWithMaxAgeZero(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	code, _, stderr := run("--max-age", "0", "--outlook-profile", "Main", "--outlook-account", teamsAccount, "calendar", "--days", "1")
	body := usageBody(t, stderr)
	if code != 2 || !strings.Contains(body["fix"].(string), "teamscrawl sync --outlook-profile Main --outlook-account "+teamsAccount) {
		t.Fatalf("%d %v", code, body)
	}
	if code, _, errOut := run("sync", "--outlook-account", teamsAccount); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := run("--max-age", "0", "--outlook-account", teamsAccount, "calendar", "--days", "1"); code != 0 {
		t.Fatalf("a link already in place: exit %d: %s", code, errOut)
	}
	// With no archive at all there is nothing pending to refuse.
	fresh := textEnv(t)
	if code, _, errOut := fresh.run("--max-age", "0", "--outlook-root", "none", "calendar", "--days", "1"); code != 0 {
		t.Fatal(errOut)
	}
}

// An ambient TEAMSCRAWL_OUTLOOK_ACCOUNT never breaks a command that neither syncs nor reads the
// calendar, and with the Outlook source off it is a warning where a flag would be an error.
func TestOutlookAccountEnvironment(t *testing.T) {
	e := textEnv(t)
	e.sync()
	t.Setenv("TEAMSCRAWL_OUTLOOK_ACCOUNT", "not-an-account")
	for _, cmd := range [][]string{{"whoami"}, {"status"}, {"--version"}} {
		if code, _, errOut := e.run(append([]string{"--max-age", "0"}, cmd...)...); code != 0 {
			t.Fatalf("%v: exit %d: %s", cmd, code, errOut)
		}
	}
	code, out, errOut := e.run("--max-age", "0", "calendar", "--days", "1")
	if code != 0 || !strings.Contains(errOut, "needs the Outlook source") || decode(t, out)["items"] == nil {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if code, _, _ = e.run("--max-age", "0", "calendar", "event", "ev_x", "--outlook-account", "a-flag"); code != 2 {
		t.Fatalf("calendar event: exit %d", code)
	}
	// The same value as a flag is a mistake, and with Outlook on a bad value is one either way.
	if code, _, _ = e.run("--max-age", "0", "--outlook-account", "a-flag", "calendar"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if code, _, _ = e.run("--max-age", "0", "--outlook-root", outlookProfiles(t, "Main"), "calendar"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
