package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const fixtureRoot = "../../testdata/teams-fixture/EBWebView"

var (
	acctA = teamsdesktop.Account{TenantID: "00000000-0000-4000-8000-000000000001", UserID: "00000000-0000-4000-8000-0000000000a1", Locale: "en-us"}
	acctB = teamsdesktop.Account{TenantID: "00000000-0000-4000-8000-000000000002", UserID: "00000000-0000-4000-8000-0000000000a2", Locale: "en-us"}
)

// Golden totals of the fixture (distinct rows in testdata/teams-fixture/expected/mapped-*.json).
const (
	fixtureMessages      = 104
	fixtureConversations = 14
	fixtureActivity      = 16
)

func newDB(t *testing.T) string { return filepath.Join(t.TempDir(), "data", "teamscrawl.db") }

// isolateTmp points snapshots at a private temp dir so leaks are detectable.
func isolateTmp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func snapshotDirs(t *testing.T, tmp string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(tmp, "teamscrawl-snapshot-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func run(t *testing.T, o Options) (Report, []Change) {
	t.Helper()
	r, ch, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return r, ch
}

func readStatus(t *testing.T, db string) store.StatusRow {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func totals(st store.StatusRow) (convs, msgs, acts int) {
	for _, a := range st.Accounts {
		convs += a.Conversations
		msgs += a.Messages
		acts += a.Activity
	}
	return
}

// copyTree copies a directory tree, preserving nothing but names and bytes.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p) //nolint:gosec // test fixture
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // G703: test copy into a temp dir
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fixtureCopy(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "EBWebView")
	copyTree(t, fixtureRoot, root)
	return root
}

func logFile(t *testing.T, root string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.leveldb", "*.log"))
	if len(m) != 1 {
		t.Fatalf("log files: %v", m)
	}
	return m[0]
}

func TestSyncFixture(t *testing.T) {
	tmp := isolateTmp(t)
	db := newDB(t)
	start := time.Now()
	r, changes := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != "ok" {
		t.Fatalf("status = %q, omissions %v", r.Status, r.Omissions)
	}
	if r.StartedAt.Before(start.Add(-time.Second)) || r.FinishedAt.Before(r.StartedAt) {
		t.Fatalf("times: %v %v", r.StartedAt, r.FinishedAt)
	}
	if r.Messages.Inserted != fixtureMessages || r.Messages.Seen != fixtureMessages ||
		r.Conversations.Inserted != fixtureConversations || r.Activity.Inserted != fixtureActivity {
		t.Fatalf("counts: msgs %+v convs %+v act %+v", r.Messages, r.Conversations, r.Activity)
	}
	if r.People.Inserted == 0 || r.People.Updated != 0 {
		t.Fatalf("people: %+v", r.People)
	}
	if len(r.Sources) != 1 || r.Sources[0].Status != "ok" || !strings.Contains(r.Sources[0].Source, "https_teams.microsoft.com_0") {
		t.Fatalf("sources: %+v", r.Sources)
	}
	if len(r.Omissions) != 0 {
		t.Fatalf("omissions: %v", r.Omissions)
	}
	st := readStatus(t, db)
	c, m, a := totals(st)
	if c != fixtureConversations || m != fixtureMessages || a != fixtureActivity || len(st.Accounts) != 2 {
		t.Fatalf("archive holds %d convs %d msgs %d activity, %d accounts", c, m, a, len(st.Accounts))
	}
	if st.LastRun == nil || st.LastRun.Status != "ok" {
		t.Fatalf("last run: %+v", st.LastRun)
	}
	// Every inserted message and activity item is reported as a new change.
	kinds := map[string]int{}
	for _, ch := range changes {
		if ch.Change != "new" || ch.Key == "" {
			t.Fatalf("change: %+v", ch)
		}
		kinds[ch.Kind]++
	}
	if kinds["message"] != fixtureMessages || kinds["activity"] != fixtureActivity {
		t.Fatalf("changes by kind: %v", kinds)
	}
	if left := snapshotDirs(t, tmp); len(left) != 0 {
		t.Fatalf("snapshot left behind: %v", left)
	}
	// Report JSON is snake_case.
	b, _ := json.Marshal(r)
	for _, want := range []string{`"status"`, `"sources"`, `"conversations"`, `"messages"`, `"people"`, `"activity"`, `"omissions"`, `"other_origins"`, `"started_at"`, `"finished_at"`, `"inserted"`, `"unchanged"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report JSON lacks %s: %s", want, b)
		}
	}
}

func TestSecondSyncUnchanged(t *testing.T) {
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	before := readStatus(t, db)
	time.Sleep(5 * time.Millisecond)
	r, changes := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != "unchanged" || len(r.Sources) != 1 || r.Sources[0].Status != "unchanged" {
		t.Fatalf("report: %+v", r)
	}
	zero := store.Counts{}
	if r.Messages != zero || r.Conversations != zero || r.People != zero || r.Activity != zero || len(changes) != 0 {
		t.Fatalf("an unchanged run wrote: %+v %v", r, changes)
	}
	after := readStatus(t, db)
	if !after.Accounts[0].LastSyncedAt.Equal(before.Accounts[0].LastSyncedAt) {
		t.Fatalf("an unchanged run touched the accounts: %v -> %v", before.Accounts[0].LastSyncedAt, after.Accounts[0].LastSyncedAt)
	}
	if after.LastRun == nil || after.LastRun.Status != "unchanged" || after.LastRun.ID <= before.LastRun.ID {
		t.Fatalf("the attempt should still be recorded: %+v", after.LastRun)
	}
}

func TestSyncAfterWrite(t *testing.T) {
	root := fixtureCopy(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(logFile(t, root), later, later); err != nil {
		t.Fatal(err)
	}
	r, changes := run(t, Options{Root: root, DBPath: db})
	if r.Status != "ok" {
		t.Fatalf("status = %q", r.Status)
	}
	if r.Messages.Seen != fixtureMessages || r.Messages.Unchanged != fixtureMessages ||
		r.Conversations.Unchanged != fixtureConversations || r.Conversations.Seen != fixtureConversations ||
		r.Activity.Unchanged != fixtureActivity || r.People.Unchanged != r.People.Seen || r.People.Seen == 0 {
		t.Fatalf("a re-decode must change no rows: msgs %+v convs %+v act %+v people %+v", r.Messages, r.Conversations, r.Activity, r.People)
	}
	if len(changes) != 0 {
		t.Fatalf("changes for unchanged rows: %v", changes)
	}
}

func TestSyncOmissions(t *testing.T) {
	root := fixtureCopy(t)
	blobs, _ := filepath.Glob(filepath.Join(root, "*", "IndexedDB", "*.blob", "*", "*", "*"))
	if len(blobs) == 0 {
		t.Fatal("fixture has no blobs")
	}
	// Corrupt the blob that an allowlisted record needs: removing each in turn, keep the one that omits.
	db := newDB(t)
	var r Report
	for _, b := range blobs {
		keep, _ := os.ReadFile(b) //nolint:gosec // test fixture copy
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
		var err error
		r, _, err = Run(context.Background(), Options{Root: root, DBPath: newDB(t)})
		if err != nil {
			t.Fatal(err)
		}
		if r.Omissions["blob_missing"] == 1 {
			break
		}
		_ = os.WriteFile(b, keep, 0o600) //nolint:gosec // G703: test fixture copy
	}
	if r.Status != "ok_with_omissions" || r.Omissions["blob_missing"] != 1 {
		t.Fatalf("status %q omissions %v", r.Status, r.Omissions)
	}
	if r.Sources[0].Status != "ok_with_omissions" || r.Sources[0].Omissions["blob_missing"] != 1 {
		t.Fatalf("source report: %+v", r.Sources[0])
	}
	_ = db
}

func TestSyncLocked(t *testing.T) {
	db := newDB(t)
	release, err := store.AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r, ch, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeLocked || coded.Exit != errs.ExitLocked {
		t.Fatalf("err = %v", err)
	}
	if r.Status != "" || ch != nil {
		t.Fatalf("a failed run returns no report: %+v", r)
	}
}

func TestSyncAccountFilter(t *testing.T) {
	db := newDB(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db, Account: &acctA})
	if r.Status != "ok" {
		t.Fatalf("status = %q", r.Status)
	}
	st := readStatus(t, db)
	if len(st.Accounts) != 1 || st.Accounts[0].UserID != acctA.UserID || st.Accounts[0].TenantID != acctA.TenantID {
		t.Fatalf("accounts: %+v", st.Accounts)
	}
	c, m, a := totals(st)
	if c == 0 || m == 0 || a == 0 || m >= fixtureMessages || c >= fixtureConversations {
		t.Fatalf("filtered archive: %d %d %d", c, m, a)
	}
	if r.Messages.Inserted != m || r.Conversations.Inserted != c || r.Activity.Inserted != a {
		t.Fatalf("report %+v vs archive %d %d %d", r, c, m, a)
	}
}

func TestFilteredRunNeverSkipsOtherAccount(t *testing.T) {
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db, Account: &acctA})
	// The same unchanged cache, now for B: must decode, not skip.
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db, Account: &acctB})
	if r.Status != "ok" || r.Messages.Inserted == 0 {
		t.Fatalf("B's run: %+v", r)
	}
	st := readStatus(t, db)
	c, m, a := totals(st)
	if len(st.Accounts) != 2 || c != fixtureConversations || m != fixtureMessages || a != fixtureActivity {
		t.Fatalf("after A then B: %d accounts, %d %d %d", len(st.Accounts), c, m, a)
	}
	// A filtered run always decodes, even when the cache is unchanged.
	r, _ = run(t, Options{Root: fixtureRoot, DBPath: db, Account: &acctB})
	if r.Status != "ok" || r.Sources[0].Status != "ok" || r.Messages.Unchanged == 0 {
		t.Fatalf("repeat filtered run: %+v", r)
	}
	// Filtered runs store no fingerprint, so an unfiltered run after them does not skip either.
	r, _ = run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != "ok" {
		t.Fatalf("unfiltered run after filtered ones: %q", r.Status)
	}
	// ...and then the unfiltered fingerprint is recorded and skips.
	r, _ = run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != "unchanged" {
		t.Fatalf("second unfiltered run: %q", r.Status)
	}
	// A filtered run after that still decodes.
	r, _ = run(t, Options{Root: fixtureRoot, DBPath: db, Account: &acctA})
	if r.Status != "ok" {
		t.Fatalf("filtered after unchanged: %q", r.Status)
	}
}

func TestDecoderVersionBumpResyncs(t *testing.T) {
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	srcs, _, err := teamsdesktop.Discover(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the newest stored fingerprint by one taken under another DecoderVersion: the
	// fingerprint hashes the version, so the same files give a different value.
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.RecordRun(context.Background(), store.Run{StartedAt: now, FinishedAt: now, Source: srcs[0].Key(), Fingerprint: "fingerprint-under-decoder-version-0", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != "ok" || r.Messages.Seen != fixtureMessages || r.Messages.Unchanged != fixtureMessages {
		t.Fatalf("a different stored fingerprint must decode again: %+v", r)
	}
}

func TestFailedRunRecorded(t *testing.T) {
	db := newDB(t)
	missing := filepath.Join(t.TempDir(), "no-teams")
	r, ch, err := Run(context.Background(), Options{Root: missing, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeTeamsNotInstalled {
		t.Fatalf("err = %v", err)
	}
	if r.Status != "" || ch != nil {
		t.Fatalf("a failed run returns no report: %+v", r)
	}
	st := readStatus(t, db)
	if st.LastRun == nil || st.LastRun.Status != "failed" {
		t.Fatalf("last run: %+v", st.LastRun)
	}
	if st.LastSuccessAt.After(time.Time{}) {
		t.Fatalf("a failed run is not a success: %v", st.LastSuccessAt)
	}
	// A failed run never becomes the fingerprint to skip against.
	if r, _ := run(t, Options{Root: fixtureRoot, DBPath: db}); r.Status != "ok" {
		t.Fatalf("status after a failure: %q", r.Status)
	}
}

func TestPauseHookAndCancel(t *testing.T) {
	tmp := isolateTmp(t)
	db := newDB(t)
	t.Setenv("TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT", "150ms")
	start := time.Now()
	run(t, Options{Root: fixtureRoot, DBPath: db})
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("the pause hook did not pause: %v", time.Since(start))
	}

	// A cancelled run stops during the pause, removes its snapshot and records a failure.
	t.Setenv("TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT", "30s")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: db, Account: &acctA})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if left := snapshotDirs(t, tmp); len(left) != 0 {
		t.Fatalf("snapshot left behind: %v", left)
	}
	if st := readStatus(t, db); st.LastRun.Status != "failed" {
		t.Fatalf("cancelled run: %+v", st.LastRun)
	}
}

func TestSweepsStaleSnapshots(t *testing.T) {
	tmp := isolateTmp(t)
	stale := filepath.Join(tmp, "teamscrawl-snapshot-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	run(t, Options{Root: fixtureRoot, DBPath: newDB(t)})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot kept: %v", err)
	}
}

func TestProgressOutput(t *testing.T) {
	var buf strings.Builder
	run(t, Options{Root: fixtureRoot, DBPath: newDB(t), Progress: &buf})
	if !strings.Contains(buf.String(), "https_teams.microsoft.com_0") {
		t.Fatalf("progress = %q", buf.String())
	}
}
