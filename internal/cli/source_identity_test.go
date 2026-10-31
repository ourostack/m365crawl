package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// runOn runs Main on db, with the env's Teams fixture, unless args name another root.
func runOn(e *env, db string, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errb bytes.Buffer
	code = Main(append([]string{"--db", db, "--teams-root", e.root}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// otherMachine gives the rest of the test a fresh user configuration directory, as another machine
// (or another user) has.
func otherMachine(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = prev })
}

func readOn(t *testing.T, e *env, db string, args ...string) map[string]any {
	t.Helper()
	code, out, stderr := runOn(e, db, args...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, stderr)
	}
	return decode(t, out)
}

// syncOn runs an explicit sync into db. db must be in a folder that does not exist yet: Windows
// refuses an existing custom parent a test made itself.
func syncOn(t *testing.T, e *env, db string) {
	t.Helper()
	if code, _, stderr := runOn(e, db, "sync"); code != 0 {
		t.Fatalf("sync %s: exit %d: %s", db, code, stderr)
	}
}

func syncRuns(t *testing.T, e *env, db string) float64 {
	t.Helper()
	m := readOn(t, e, db, "--no-sync", "sql", "select count(*) as n from sync_runs")
	return m["rows"].([]any)[0].([]any)[0].(float64)
}

func archiveSQL(t *testing.T, db, stmt string) {
	t.Helper()
	h, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func hasNotice(m map[string]any, parts ...string) bool {
	notices, _ := m["notices"].([]any)
	for _, n := range notices {
		s, _ := n.(string)
		found := true
		for _, p := range parts {
			found = found && strings.Contains(s, p)
		}
		if found {
			return true
		}
	}
	return false
}

func TestReadOnAMissingArchiveCreatesNothing(t *testing.T) {
	e := newEnv(t)
	scratch := filepath.Join(t.TempDir(), "scratch", "m365crawl.db")
	for _, args := range [][]string{{"messages"}, {"status"}, {"notes", "list"}} {
		m := readOn(t, e, scratch, args...)
		if m["implicit_sync"] != implicitSyncNoArchive || !hasNotice(m, "There is no archive at "+scratch, "--db "+quoteArg(scratch), " sync`") {
			t.Fatalf("%v on a missing archive = %v", args, m)
		}
		if _, err := os.Stat(filepath.Dir(scratch)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v created the archive or its folder: %v", args, err)
		}
	}
	if m := readOn(t, e, scratch, "messages"); m["needs_sync"] != true || len(m["notices"].([]any)) != 1 {
		t.Fatalf("one notice and the sync hint = %v", m)
	}
	// --no-sync still says disabled: it was asked for, and nothing would have synced anyway.
	if m := readOn(t, e, scratch, "--no-sync", "messages"); m["implicit_sync"] != implicitSyncDisabled {
		t.Fatalf("--no-sync on a missing archive = %v", m)
	}
}

func TestReadOnAnArchiveThisMachineSyncedKeepsSyncing(t *testing.T) {
	e := newEnv(t)
	own := filepath.Join(t.TempDir(), "own", "m365crawl.db")
	syncOn(t, e, own)
	if m := readOn(t, e, own, "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncRan || m["notices"] != nil {
		t.Fatalf("a read of this machine's archive = %v", m)
	}
	// A copy of it, even on this machine, is another archive: a forensic or scratch copy stays as
	// it was copied.
	raw, err := os.ReadFile(own) //nolint:gosec // G304: the test's own archive
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copied, raw, 0o600); err != nil { //nolint:gosec // G703: under the test's temp dir
		t.Fatal(err)
	}
	if m := readOn(t, e, copied, "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncForeignArchive {
		t.Fatalf("a local copy of this machine's archive = %v", m)
	}
	// The same archive named by a relative path from its folder.
	t.Chdir(filepath.Dir(own))
	if m := readOn(t, e, filepath.Base(own), "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncRan {
		t.Fatalf("a relative --db = %v", m)
	}
}

func TestReadOnACopyFromAnotherMachineDoesNotSync(t *testing.T) {
	e := newEnv(t)
	copied := filepath.Join(t.TempDir(), "copy", "m365crawl.db")
	otherMachine(t)
	syncOn(t, e, copied)
	otherMachine(t)
	before := syncRuns(t, e, copied)
	m := readOn(t, e, copied, "--max-age", "1ns", "messages")
	if m["implicit_sync"] != implicitSyncForeignArchive || m["synced"] != nil || m["count"].(float64) == 0 ||
		!hasNotice(m, "This read did not sync", copied, "--db "+quoteArg(copied), " sync`") {
		t.Fatalf("a copied archive = %v", m)
	}
	if m := readOn(t, e, copied, "--max-age", "1ns", "notes", "list"); m["implicit_sync"] != implicitSyncForeignArchive || !hasNotice(m, "This read did not sync") {
		t.Fatalf("a registered-source read of a copied archive = %v", m)
	}
	if m := readOn(t, e, copied, "--max-age", "1ns", "search", "--source", "all", "budget"); m["implicit_sync"] != implicitSyncForeignArchive || len(m["notices"].([]any)) != 1 {
		t.Fatalf("a search that refreshes registered sources too = %v", m)
	}
	code, _, stderr := runOn(e, copied, "--max-age", "1ns", "--format", "text", "messages")
	if code != 0 || !strings.Contains(stderr, "hint: this read did not sync: this machine's sources do not feed this archive") {
		t.Fatalf("text mode = %d %q", code, stderr)
	}
	if after := syncRuns(t, e, copied); after != before {
		t.Fatalf("the copied archive was written: %v sync runs, then %v", before, after)
	}
	// An explicit sync adopts it; later reads keep it fresh.
	syncOn(t, e, copied)
	if m := readOn(t, e, copied, "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncRan {
		t.Fatalf("after an explicit sync = %v", m)
	}
}

func TestReadWithAnotherTeamsRootDoesNotSync(t *testing.T) {
	e := newEnv(t)
	own := filepath.Join(t.TempDir(), "own", "m365crawl.db")
	syncOn(t, e, own)
	code, out, stderr := runOn(e, own, "--max-age", "1ns", "--teams-root", filepath.Join(t.TempDir(), "elsewhere"), "messages")
	if m := decode(t, out); code != 0 || m["implicit_sync"] != implicitSyncForeignArchive || stderr != "" {
		t.Fatalf("another Teams root = %d %v %q", code, m, stderr)
	}
}

func TestLegacyArchiveIsAdoptedOnlyWhenItsTeamsSourcesAreHere(t *testing.T) {
	e := newEnv(t)
	legacy := filepath.Join(t.TempDir(), "legacy", "m365crawl.db")
	syncOn(t, e, legacy)
	archiveSQL(t, legacy, `delete from meta where key like 'source_identity:%'`)
	if m := readOn(t, e, legacy, "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncRan {
		t.Fatalf("a legacy archive of this machine's sources = %v", m)
	}
	st, err := store.OpenReadOnly(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SourceIdentity(context.Background())
	_ = st.Close()
	if err != nil || len(id.Recorded) != 1 {
		t.Fatalf("the adopting sync recorded %+v, %v", id, err)
	}
	other := filepath.Join(t.TempDir(), "other", "m365crawl.db")
	syncOn(t, e, other)
	archiveSQL(t, other, `delete from meta where key like 'source_identity:%'`)
	archiveSQL(t, other, `update sync_runs set source = 'Elsewhere|https_teams.microsoft.com_0' where source like '%|https_teams.%'`)
	if m := readOn(t, e, other, "--max-age", "1ns", "messages"); m["implicit_sync"] != implicitSyncForeignArchive {
		t.Fatalf("a legacy archive of other sources = %v", m)
	}
}

// The sync records the keys of the sources it reads: the explicit sync, the implicit sync and
// watch all build their options here.
func TestSyncOptionsCarryTheSourceIdentity(t *testing.T) {
	otherMachine(t)
	rt := &runtime{g: &Globals{}, root: "", outlookOn: true}
	o := rt.syncOptions(syncer.Options{})
	if len(o.SourceIdentity) != 2 || !strings.HasPrefix(o.SourceIdentity[0], "teams:") || !strings.HasPrefix(o.SourceIdentity[1], "outlook:") {
		t.Fatalf("keys = %v", o.SourceIdentity)
	}
	again := rt.sourceIdentity(false)
	if len(again) != 2 || again[0] != o.SourceIdentity[0] || again[1] != o.SourceIdentity[1] {
		t.Fatalf("the machine ID is kept: %v then %v", o.SourceIdentity, again)
	}
	rt.outlookOn = false
	if keys := rt.sourceIdentity(false); len(keys) != 1 || keys[0] != o.SourceIdentity[0] {
		t.Fatalf("Outlook off = %v", keys)
	}
	otherMachine(t)
	if keys := rt.sourceIdentity(false); keys != nil {
		t.Fatalf("a read never makes a machine ID: %v", keys)
	}
}

func TestMachineIDFailures(t *testing.T) {
	prev := userConfigDir
	t.Cleanup(func() { userConfigDir = prev })
	userConfigDir = func() (string, error) { return "", errors.New("no configuration directory") }
	if id := machineID(true); id != "" {
		t.Fatalf("no configuration directory = %q", id)
	}
	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	userConfigDir = func() (string, error) { return blocker, nil }
	if id := machineID(true); id != "" {
		t.Fatalf("an unusable directory = %q", id)
	}
	dir := filepath.Join(base, "config")
	userConfigDir = func() (string, error) { return dir, nil }
	prevLink := linkFile
	t.Cleanup(func() { linkFile = prevLink })
	linkFile = func(string, string) error { return errors.New("no links here") }
	if id := machineID(true); id != "" {
		t.Fatalf("a failed link = %q", id)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "m365crawl", ".machine-id-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	linkFile = prevLink
	if err := os.WriteFile(filepath.Join(dir, "m365crawl", "machine-id"), []byte("not hex"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := machineID(true); id != "" {
		t.Fatalf("a damaged ID is not used or replaced: %q", id)
	}
	if err := os.WriteFile(filepath.Join(dir, "m365crawl", "machine-id"), []byte(strings.Repeat("ab", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := machineID(false); id != strings.Repeat("ab", 16) {
		t.Fatalf("an existing ID = %q", id)
	}
	readOnly := filepath.Join(base, "read-only", "m365crawl")
	if err := os.MkdirAll(readOnly, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnly, 0o500); err == nil && os.Getuid() > 0 { //nolint:gosec // G302: a directory, read-only on purpose
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) }) //nolint:gosec // G302: a directory
		userConfigDir = func() (string, error) { return filepath.Dir(readOnly), nil }
		if id := machineID(true); id != "" {
			t.Fatalf("a read-only directory = %q", id)
		}
	}
}

func TestFedRules(t *testing.T) {
	otherMachine(t)
	rt := &runtime{g: &Globals{}, root: "/teams", outlookOn: true, outlookRoot: "", outlookDefault: true}
	keys := rt.sourceIdentity(true)
	teams, outlook := keys[0], keys[1]
	for _, tc := range []struct {
		name     string
		recorded []string
		byDef    bool
		want     bool
	}{
		{"both recorded", []string{teams, outlook}, true, true},
		{"no Teams key", []string{outlook}, true, false},
		{"default Outlook, never recorded", []string{teams}, true, true},
		{"named Outlook, never recorded", []string{teams}, false, false},
		{"another Outlook recorded", []string{teams, "outlook:other"}, true, false},
	} {
		rt.outlookDefault = tc.byDef
		id := store.SourceIdentity{Recorded: map[string]bool{}}
		for _, k := range tc.recorded {
			id.Recorded[k] = true
		}
		if got := rt.fed(id); got != tc.want {
			t.Errorf("%s = %v", tc.name, got)
		}
	}
	otherMachine(t)
	if rt.fed(store.SourceIdentity{Recorded: map[string]bool{teams: true}}) {
		t.Fatal("no machine ID here, yet fed")
	}
}

func TestLegacyFedDiscoversTheDefaultTeamsRoot(t *testing.T) {
	prev := discover
	t.Cleanup(func() { discover = prev })
	var asked string
	discover = func(root string) ([]teamsdesktop.Source, []string, error) {
		asked = root
		return []teamsdesktop.Source{{Profile: "P", Origin: "https_teams.microsoft.com_0"}}, nil, nil
	}
	rt := &runtime{g: &Globals{}}
	if !rt.legacyFed([]string{"P|https_teams.microsoft.com_0"}) || asked != teamsdesktop.DefaultRoot() {
		t.Fatalf("default root %q", asked)
	}
	// Profiles come and go, and Teams moves origins: one profile of the history that is here is
	// enough.
	if !rt.legacyFed([]string{"Gone|https_teams.microsoft.com_0", "P|https_teams.cloud.microsoft_0"}) {
		t.Fatal("a moved origin and a profile that is gone")
	}
	if rt.legacyFed(nil) || rt.legacyFed([]string{"Other|https_teams.microsoft.com_0"}) {
		t.Fatal("no sync history, or only other profiles, is no evidence")
	}
	discover = func(string) ([]teamsdesktop.Source, []string, error) { return nil, nil, errors.New("no Teams") }
	if rt.legacyFed([]string{"P|https_teams.microsoft.com_0"}) {
		t.Fatal("fed without Teams")
	}
}

func TestRootRefAndDefaultArchive(t *testing.T) {
	rt := &runtime{g: &Globals{}}
	tmp := t.TempDir()
	if rt.rootRef("") != "" || rt.rootRef(filepath.Join(tmp, "a", "..", "b")) != filepath.Join(tmp, "b") {
		t.Fatal("root refs")
	}
	prev := getwd
	t.Cleanup(func() { getwd = prev })
	getwd = func() (string, error) { return filepath.Join(string(filepath.Separator), "wd"), nil }
	if got := rt.rootRef("x"); got != filepath.Join(string(filepath.Separator), "wd", "x") {
		t.Fatalf("relative = %q", got)
	}
	getwd = func() (string, error) { return "", errors.New("no working directory") }
	if got := rt.rootRef("./x"); got != "x" {
		t.Fatalf("no working directory = %q", got)
	}
	getwd = prev
	if !rt.isDefaultArchive() {
		t.Fatal("no --db is the default archive")
	}
	def, err := defaultArchivePath()
	if err != nil {
		t.Fatal(err)
	}
	rt.g.DB, rt.dbPath = def, def
	if !rt.isDefaultArchive() {
		t.Fatal("--db naming the default archive")
	}
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(def) })
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Link(def, link); err == nil {
		rt.g.DB, rt.dbPath = link, link
		if !rt.isDefaultArchive() {
			t.Fatal("another name for the default archive file")
		}
	}
	for _, k := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA"} {
		t.Setenv(k, "")
	}
	if rt.isDefaultArchive() {
		t.Fatal("no default archive, yet default")
	}
}

func TestCheckFeedReportsAnUnreadableArchive(t *testing.T) {
	e := newEnv(t)
	bad := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(bad, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"messages"}, {"notes", "list"}} {
		if code, _, stderr := runOn(e, bad, append([]string{"--max-age", "1ns"}, args...)...); code == 0 || stderr == "" {
			t.Fatalf("%v on an unreadable archive = %d %q", args, code, stderr)
		}
	}
}

func TestNotFedNoticeWithoutAnExecutableSync(t *testing.T) {
	e := newEnv(t)
	t.Chdir(t.TempDir())
	prev := getwd
	t.Cleanup(func() { getwd = prev })
	getwd = func() (string, error) { return "", errors.New("no working directory") }
	m := readOn(t, e, "scratch.db", "messages")
	if m["implicit_sync"] != implicitSyncNoArchive || !hasNotice(m, "There is no archive at scratch.db", "run `m365crawl --db scratch.db sync`") {
		t.Fatalf("no working directory = %v", m)
	}
}

func TestCanonicalPathResolvesLinksAsFarAsThePathExists(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symbolic links here")
	}
	want := filepath.Join(canonicalPath(real), "not", "yet.db")
	if got := canonicalPath(filepath.Join(link, "not", "yet.db")); got != want {
		t.Fatalf("through a link = %q, want %q", got, want)
	}
}
