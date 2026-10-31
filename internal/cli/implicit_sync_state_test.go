package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
)

// ageArchive makes the archive's only complete sync look the given SQLite modifier old ('-8 minutes').
func (e *env) ageArchive(modifier string) {
	e.t.Helper()
	e.exec(`update sync_runs set finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','` + modifier + `'), started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','` + modifier + `','-1 seconds')`)
}

// Issue #264: an agent that asked for fresh data with --max-age 0 read an 8-minute-old archive and
// got an empty answer with nothing saying the read had skipped its sync.
func TestMaxAgeZeroReadOnStaleArchiveSaysItDidNotSync(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.ageArchive("-8 minutes")
	before := syncRunCount(t, e)
	code, stdout, stderr := e.run("--max-age", "0", "sql", "select count(*) as n from messages where 0", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := syncRunCount(t, e); n != before {
		t.Fatalf("--max-age 0 keeps its 1.x meaning and must not sync: runs %d -> %d", before, n)
	}
	m := decode(t, stdout)
	if m["implicit_sync"] != "disabled" {
		t.Fatalf("implicit_sync = %v, want disabled: %s", m["implicit_sync"], stdout)
	}
	notices := noticesOf(m)
	if len(notices) != 1 || !strings.Contains(notices[0], "did not sync") || !strings.Contains(notices[0], "--max-age 0") ||
		!strings.Contains(notices[0], "--no-sync") || !strings.Contains(notices[0], "8m old") || !strings.Contains(notices[0], " sync`") {
		t.Fatalf("notices = %q", notices)
	}
	if stderr != "" {
		t.Fatalf("JSON mode carries the caution in the result, not on stderr: %q", stderr)
	}
	// Text mode prints the notice and a short stderr hint, so an empty answer cannot pass as fresh.
	_, stdout, stderr = e.run("--format", "text", "--max-age", "0", "sql", "select 1 as n where 0")
	if !strings.Contains(stdout, "notice: ") || !strings.HasPrefix(stderr, "hint: this read did not sync") || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("text stdout %q stderr %q", stdout, stderr)
	}
}

func TestNoSyncReadsTheArchiveAsItIs(t *testing.T) {
	e := newEnv(t)
	e.sync()
	before := syncRunCount(t, e)
	// A fresh archive read with --no-sync says so and adds no caution.
	_, stdout, stderr := e.run("--no-sync", "messages", "--limit", "1")
	m := decode(t, stdout)
	if m["implicit_sync"] != "disabled" || m["notices"] != nil || m["synced"] != nil || stderr != "" {
		t.Fatalf("fresh --no-sync: stdout %s stderr %q", stdout, stderr)
	}
	_, _, stderr = e.run("--format", "text", "--no-sync", "messages", "--limit", "1")
	if stderr != "" {
		t.Fatalf("fresh --no-sync text stderr %q", stderr)
	}
	// Older than the default max age: a notice, and a stderr hint in text mode. --no-sync wins
	// over any --max-age.
	e.ageArchive("-134 minutes")
	_, stdout, stderr = e.run("--no-sync", "--max-age", "1ns", "messages", "--limit", "1")
	m = decode(t, stdout)
	notices := noticesOf(m)
	if m["implicit_sync"] != "disabled" || len(notices) != 1 || !strings.Contains(notices[0], "--no-sync") ||
		!strings.Contains(notices[0], "2h14m old") || strings.Contains(notices[0], "--max-age 0") || stderr != "" {
		t.Fatalf("stale --no-sync: stdout %s stderr %q", stdout, stderr)
	}
	_, _, stderr = e.run("--format", "text", "--no-sync", "messages", "--limit", "1")
	if !strings.HasPrefix(stderr, "hint: this read did not sync; the archive is 2h14m old") {
		t.Fatalf("stale --no-sync text stderr %q", stderr)
	}
	// The environment variable does the same.
	t.Setenv("M365CRAWL_NO_SYNC", "1")
	_, stdout, _ = e.run("messages", "--limit", "1")
	if decode(t, stdout)["implicit_sync"] != "disabled" {
		t.Fatalf("M365CRAWL_NO_SYNC: %s", stdout)
	}
	if n := syncRunCount(t, e); n != before {
		t.Fatalf("--no-sync must never sync: runs %d -> %d", before, n)
	}
}

func TestImplicitSyncStateRanAndSkippedFresh(t *testing.T) {
	e := newEnv(t)
	_, stdout, _ := e.run("messages", "--limit", "1")
	if m := decode(t, stdout); m["implicit_sync"] != "ran" || m["synced"] == nil || m["notices"] != nil {
		t.Fatalf("first read: %s", stdout)
	}
	_, stdout, _ = e.run("messages", "--limit", "1")
	if m := decode(t, stdout); m["implicit_sync"] != "skipped_fresh" || m["synced"] != nil {
		t.Fatalf("fresh read: %s", stdout)
	}
	_, stdout, _ = e.run("whoami")
	m := decode(t, stdout)
	if archive, _ := m["archive"].(map[string]any); m["implicit_sync"] != "skipped_fresh" || archive["implicit_sync"] != "skipped_fresh" {
		t.Fatalf("whoami: %s", stdout)
	}
}

func TestNeverSyncedNoSyncReadKeepsNeedsSyncWithoutExtraNotice(t *testing.T) {
	e := newEnv(t)
	_, stdout, _ := e.run("--no-sync", "messages")
	m := decode(t, stdout)
	if m["implicit_sync"] != "disabled" || m["needs_sync"] != true || m["notices"] != nil {
		t.Fatalf("never synced --no-sync: %s", stdout)
	}
}

func TestEmittedArchiveCommandsUseNoSyncOnlyForReads(t *testing.T) {
	read := archiveScopedCommand("/a.db", "--max-text", "0", "sql", "select 1")
	if read != "m365crawl --db /a.db --no-sync --max-text 0 sql "+quoteArg("select 1") {
		t.Fatalf("read command = %q", read)
	}
	for _, args := range [][]string{
		{"sync"},
		{"--format", "json", "--teams-root", "/t", "sync", "--outlook-account", "x/y"},
		{"calendar", "attendees", "sync", "--source-id", "3"},
		{"loop", "sync"},
		{"docs", "sync", "--source-id", "2"},
		{"content", "fetch", "page", "--registration-id", "1"},
		{"content", "capture", "--file", "/f.docx"},
		{"content", "signin", "https://example.invalid"},
		{"--format", "json", "transcripts", "fetch", "event-1"},
	} {
		got := archiveScopedCommand("/a.db", args...)
		if strings.Contains(got, "--no-sync") || strings.Contains(got, "--max-age") {
			t.Errorf("acquisition command %q carries a freshness flag", got)
		}
	}
	for _, args := range [][]string{{"calendar", "attendees", "sources"}, {"loop", "sources", "--source-id", "1"}, {"content", "show", "x"}, {"--format", "text", "people", "sources"}} {
		if got := archiveScopedCommand("/a.db", args...); !strings.Contains(got, " --no-sync ") || strings.Contains(got, "--max-age") {
			t.Errorf("read command %q", got)
		}
	}
}

func TestHelpTeachesNoSyncNotMaxAgeZero(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"--help"}, {"sql", "--help"}, {"loop", "--help"}, {"skill"}} {
		_, stdout, _ := e.run(args...)
		if !strings.Contains(stdout, "--no-sync") {
			t.Errorf("%v must teach --no-sync:\n%s", args, stdout)
		}
		// --max-age 0 may appear only to warn that it is not a fresh read.
		for _, line := range strings.Split(stdout, "\n") {
			if strings.Contains(line, "--max-age 0") && !strings.Contains(line, "does not mean fresh") {
				t.Errorf("%v teaches --max-age 0: %q", args, line)
			}
		}
	}
}

func TestNoSyncEnvironmentValues(t *testing.T) {
	e := newEnv(t)
	e.sync()
	t.Setenv("M365CRAWL_NO_SYNC", "maybe")
	code, _, stderr := e.run("messages")
	if body := usageBody(t, stderr); code != 2 || !strings.Contains(body["message"].(string), "M365CRAWL_NO_SYNC") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	t.Setenv("M365CRAWL_NO_SYNC", "0")
	_, stdout, _ := e.run("messages")
	if decode(t, stdout)["implicit_sync"] != "skipped_fresh" {
		t.Fatalf("M365CRAWL_NO_SYNC=0: %s", stdout)
	}
}

// A read by Outlook account has no executable sync command, so the caution names plain sync.
func TestSyncOffNoticeWithoutAnExecutableSyncCommand(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	e.ageArchive("-3 hours")
	code, stdout, stderr := run("--json", "--no-sync", "--account", "outlook/Main", "calendar", "--days", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	notices := noticesOf(decode(t, stdout))
	if len(notices) == 0 || !strings.Contains(notices[len(notices)-1], "run `m365crawl sync` first") {
		t.Fatalf("notices = %q", notices)
	}
}

func TestOutlookLinkRefusedWithNoSync(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	code, _, stderr := run("--no-sync", "--outlook-profile", "Main", "--outlook-account", teamsAccount, "calendar", "--days", "1")
	if body := usageBody(t, stderr); code != 2 || !strings.Contains(body["message"].(string), "(--no-sync)") {
		t.Fatalf("%d %v", code, body)
	}
}

// Where the sync was turned off decides what the notice tells the agent to undo, and an explicit
// flag always beats the environment.
func TestSyncOffSourcesAndPrecedence(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.ageArchive("-3 hours")
	read := func(env map[string]string, args ...string) map[string]any {
		t.Helper()
		for k, v := range env {
			t.Setenv(k, v)
		}
		code, stdout, stderr := e.run(append(args, "--json", "conversations", "--limit", "1")...)
		for k := range env {
			t.Setenv(k, "")
		}
		if code != 0 {
			t.Fatalf("%v %v: exit %d: %s", env, args, code, stderr)
		}
		return decode(t, stdout)
	}
	noSyncEnv := map[string]string{"M365CRAWL_NO_SYNC": "1"}
	maxAgeEnv := map[string]string{"M365CRAWL_MAX_AGE": "0"}
	for _, tc := range []struct {
		name          string
		env           map[string]string
		args          []string
		want, names   string
		drop, notDrop string
	}{
		{"env no-sync", noSyncEnv, nil, "disabled", "(M365CRAWL_NO_SYNC)", "unset M365CRAWL_NO_SYNC", "drop --no-sync"},
		{"env max-age 0", maxAgeEnv, nil, "disabled", "M365CRAWL_MAX_AGE=0 turns", "unset M365CRAWL_MAX_AGE", "drop --max-age 0"},
		{"flag no-sync over env max-age", maxAgeEnv, []string{"--no-sync"}, "disabled", "(--no-sync)", "drop --no-sync", "M365CRAWL"},
		{"flag max-age 0 over env no-sync", noSyncEnv, []string{"--max-age", "0"}, "disabled", "--max-age 0 turns", "drop --max-age 0", "M365CRAWL"},
		{"explicit max-age beats env no-sync", noSyncEnv, []string{"--max-age", "1ns"}, "ran", "", "", ""},
		{"--no-sync=false beats env no-sync", noSyncEnv, []string{"--no-sync=false", "--max-age", "1ns"}, "ran", "", "", ""},
		{"--no-sync=false alone beats env no-sync", noSyncEnv, []string{"--no-sync=false"}, "skipped_fresh", "", "", ""},
	} {
		m := read(tc.env, tc.args...)
		if m["implicit_sync"] != tc.want {
			t.Errorf("%s: implicit_sync = %v", tc.name, m["implicit_sync"])
			continue
		}
		notices := noticesOf(m)
		if tc.names == "" {
			if len(notices) != 0 {
				t.Errorf("%s: notices %q", tc.name, notices)
			}
			continue
		}
		if len(notices) != 1 || !strings.Contains(notices[0], tc.names) || !strings.Contains(notices[0], tc.drop) || strings.Contains(notices[0], tc.notDrop) {
			t.Errorf("%s: notices %q", tc.name, notices)
		}
	}
	// The text hint names the real cause too.
	e.ageArchive("-3 hours")
	t.Setenv("M365CRAWL_NO_SYNC", "1")
	_, _, stderr := e.run("--format", "text", "conversations", "--limit", "1")
	if !strings.Contains(stderr, "unset M365CRAWL_NO_SYNC") || strings.Contains(stderr, "drop --no-sync") {
		t.Fatalf("env text hint %q", stderr)
	}
	t.Setenv("M365CRAWL_NO_SYNC", "")
	t.Setenv("M365CRAWL_MAX_AGE", "0")
	_, _, stderr = e.run("--format", "text", "conversations", "--limit", "1")
	if !strings.Contains(stderr, "M365CRAWL_MAX_AGE=0 turns syncing off") {
		t.Fatalf("max-age env text hint %q", stderr)
	}
}

func TestOutlookLinkRefusalNamesTheEnvironment(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	t.Setenv("M365CRAWL_NO_SYNC", "true")
	code, _, stderr := run("--outlook-profile", "Main", "--outlook-account", teamsAccount, "calendar", "--days", "1")
	if body := usageBody(t, stderr); code != 2 || !strings.Contains(body["message"].(string), "(M365CRAWL_NO_SYNC)") {
		t.Fatalf("%d %v", code, body)
	}
}

// Registered-source reads skip their own refresh under --no-sync or --max-age 0; their coverage
// objects cannot tell "skipped" from "not due", so the result always says implicit_sync: disabled
// (#264 review). The notice comes only where it can matter.
func TestRegisteredSourceReadsSayTheyDidNotRefresh(t *testing.T) {
	e := newEnv(t)
	e.sync()
	read := func(args ...string) (map[string]any, string) {
		t.Helper()
		code, stdout, stderr := e.run(append([]string{"--json"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		return decode(t, stdout), stderr
	}
	unregistered := [][]string{
		{"notes", "list"}, {"notes", "sources"}, {"search", "words", "--source", "notes"},
		{"docs", "list"}, {"search", "words", "--source", "documents"},
		{"loop", "list"}, {"search", "words", "--source", "loop"},
	}
	// No registered source: nothing was skipped that could hold newer items, so no notice.
	for _, args := range unregistered {
		for _, off := range [][]string{{"--no-sync"}, {"--max-age", "0"}} {
			m, stderr := read(append(off, args...)...)
			if m["implicit_sync"] != "disabled" || m["notices"] != nil || stderr != "" {
				t.Errorf("%v %v: implicit_sync %v notices %v stderr %q", off, args, m["implicit_sync"], m["notices"], stderr)
			}
		}
		if m, _ := read(args...); m["implicit_sync"] != nil {
			t.Errorf("%v: an ordinary refresh adds no implicit_sync: %v", args, m["implicit_sync"])
		}
	}
	peopleFixture(t, e) // a native people source attempted just now
	// The overview prints `m365crawl --no-sync people sources`; following it on a fresh source says
	// disabled and nothing else.
	if _, stdout, _ := e.run("--json"); !strings.Contains(stdout, "m365crawl --no-sync people sources") {
		t.Fatalf("overview route changed: %s", stdout)
	}
	for _, args := range [][]string{{"people", "sources"}, {"people"}} {
		m, stderr := read(append([]string{"--no-sync"}, args...)...)
		if m["implicit_sync"] != "disabled" || strings.Contains(strings.Join(noticesOf(m), " "), "did not refresh") || stderr != "" {
			t.Errorf("fresh --no-sync %v: implicit_sync %v notices %q", args, m["implicit_sync"], noticesOf(m))
		}
		// A zero max age always cautions: agents read it as "fresh". (With Teams people archived,
		// `people` is a sync-backed read and carries that read's own --max-age 0 notice.)
		m, _ = read(append([]string{"--max-age", "0"}, args...)...)
		if notices := noticesOf(m); m["implicit_sync"] != "disabled" || len(notices) == 0 || !strings.Contains(strings.Join(notices, " "), "--max-age 0 turns the") {
			t.Errorf("fresh --max-age 0 %v: notices %q", args, notices)
		}
	}
	// The source was last attempted long ago: --no-sync now cautions, naming when.
	e.exec(`update knowledge_attempts set started_at='2026-10-11T03:00:00.000Z'`)
	m, _ := read("--no-sync", "people", "sources")
	if notices := noticesOf(m); len(notices) == 0 || !strings.Contains(notices[len(notices)-1], "(--no-sync) and the oldest was last read at 2026-10-11T03:00:00Z") ||
		!strings.Contains(notices[len(notices)-1], "drop --no-sync") {
		t.Errorf("stale --no-sync: notices %q", notices)
	}
	_, _, stderr := e.run("--format", "text", "--no-sync", "people", "sources")
	if !strings.HasPrefix(stderr, "hint: this read did not refresh its registered native people sources (--no-sync)") {
		t.Fatalf("text hint %q", stderr)
	}
}

// A source never attempted counts as old as the archive's last complete sync, or as stale when
// the archive cannot say.
func TestRegisteredSyncOffNeverAttemptedSource(t *testing.T) {
	e := newEnv(t)
	e.sync()
	check := func(db string, want bool) {
		t.Helper()
		rt := &runtime{ctx: t.Context(), g: &Globals{}, dbPath: db, now: time.Now, syncOff: syncOffNoSync, format: output.JSON}
		res := newList(nil, false)
		rt.noteRegisteredSyncOff(res, "notes", []*time.Time{nil})
		if res.ImplicitSync != implicitSyncDisabled || (len(res.Notices) == 1) != want {
			t.Errorf("%s: implicit_sync %q notices %q", db, res.ImplicitSync, res.Notices)
		}
		if want && !strings.Contains(res.Notices[0], "has not been read since") && !strings.Contains(res.Notices[0], "last read") {
			t.Errorf("%s: notice %q", db, res.Notices[0])
		}
	}
	check(e.db, false) // the archive synced just now
	e.ageArchive("-3 hours")
	check(e.db, true)
	check(filepath.Join(t.TempDir(), "missing.db"), true) // no archive age at all
}

func TestCoverageRouteIsAnAcquisition(t *testing.T) {
	if got := archiveScopedCommand("/a.db", "workiq", "coverage", "--source", "chats"); strings.Contains(got, "--no-sync") {
		t.Fatalf("coverage route %q", got)
	}
	if got := archiveScopedCommand("/a.db", "--format", "json", "status"); !strings.Contains(got, " --no-sync ") {
		t.Fatalf("status read %q", got)
	}
}
