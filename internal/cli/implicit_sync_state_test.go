package cli

import (
	"strings"
	"testing"
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
