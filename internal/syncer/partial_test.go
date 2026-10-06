package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// failNthSource makes the n-th source that reaches its snapshot fail with a coded error while it
// is being applied. Sources run in key order, so with twoSourceRoot the cloud origin is first.
func failNthSource(t *testing.T, n int) {
	t.Helper()
	seen := 0
	oldAfter := afterSnapshot
	afterSnapshot = func() { seen++ }
	hookFlush(t, 2000, func(string, int) error {
		if seen == n {
			return errs.StoreMissing("injected for the partial sync test")
		}
		return nil
	})
	t.Cleanup(func() { afterSnapshot = oldAfter })
}

func runRows(t *testing.T, db, query string) []string {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cols, rows, _, err := s.SQL(context.Background(), query, 100)
	if err != nil || len(cols) != 1 {
		t.Fatalf("%s: %v %v", query, cols, err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r[0].(string))
	}
	return out
}

func TestPartialSyncKeepsTheCommittedSourcesChanges(t *testing.T) {
	root, _ := twoSourceRoot(t)
	db := newDB(t)
	failNthSource(t, 2)
	rep, changes, err := Run(context.Background(), Options{Root: root, DBPath: db})
	codedErr(t, err, errs.CodePartialSync)
	var coded *errs.Coded
	_ = errors.As(err, &coded)
	if coded.Exit != errs.ExitRuntime || !strings.Contains(coded.Message, "https_teams.microsoft.com_0") || !strings.Contains(coded.Message, errs.CodeStoreMissing) || !strings.Contains(coded.Fix, "teamscrawl doctor") {
		t.Fatalf("partial error: %+v", coded)
	}
	if rep.Status != StatusPartial || len(rep.Sources) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	ok, bad := sourceNamed(t, rep, "https_teams.cloud.microsoft_0"), sourceNamed(t, rep, "https_teams.microsoft.com_0")
	if ok.Status != StatusOK || ok.Counts == nil || ok.Counts.Messages.Inserted != fixtureMessages || len(ok.Accounts) != 2 || ok.Error != nil {
		t.Fatalf("committed source: %+v", ok)
	}
	if bad.Status != "failed" || bad.Error == nil || bad.Error.Code != errs.CodeStoreMissing || bad.Error.Message == "" || bad.Counts != nil {
		t.Fatalf("failed source: %+v", bad)
	}
	if rep.Messages.Inserted != fixtureMessages || len(changes) != fixtureMessages+fixtureActivity {
		t.Fatalf("the committed source's rows and changes must be returned: %+v %d", rep.Messages, len(changes))
	}
	// The data is in the archive, but the run refreshed nobody: the failed source's accounts stay stale.
	st := readStatus(t, db)
	if c, m, a := totals(st); m != fixtureMessages || c == 0 || a == 0 {
		t.Fatalf("archive rows: %d %d %d", c, m, a)
	}
	if !st.LastSuccessAt.IsZero() || st.LastRun == nil || st.LastRun.Status != StatusPartial {
		t.Fatalf("a partial run must not refresh: last success %v, last run %+v", st.LastSuccessAt, st.LastRun)
	}
	if got := runRows(t, db, `select status from sync_runs where source <> '' order by id`); strings.Join(got, ",") != "failed,ok" && strings.Join(got, ",") != "ok,failed" {
		t.Fatalf("per-source rows: %v", got)
	}

	// The next run retries only the failed source: the committed one is unchanged, and nothing is lost.
	beforeFlush = func(string, int) error { return nil }
	rep, changes, err = Run(context.Background(), Options{Root: root, DBPath: db})
	if err != nil || rep.Status != StatusOK || sourceNamed(t, rep, "https_teams.cloud.microsoft_0").Status != StatusUnchanged || sourceNamed(t, rep, "https_teams.microsoft.com_0").Status != StatusOK {
		t.Fatalf("retry: %v %+v", err, rep)
	}
	if len(changes) != 0 {
		t.Fatalf("the second origin holds the same rows, so nothing is new: %d changes", len(changes))
	}
	if st := readStatus(t, db); st.LastSuccessAt.IsZero() {
		t.Fatal("a full run refreshes")
	}
}

func TestFilteredSyncRefreshesOnlyItsAccount(t *testing.T) {
	db := newDB(t)
	acct := &acctA
	run(t, Options{Root: fixtureRoot, DBPath: db, Account: acct})
	s, _ := store.OpenReadOnly(context.Background(), db)
	defer func() { _ = s.Close() }()
	got, err := s.LastSuccessFor(context.Background(), acct.TenantID+"/"+acct.UserID)
	if err != nil || got.IsZero() {
		t.Fatalf("filtered account: %v %v", got, err)
	}
	if got, _ := s.LastSuccessFor(context.Background(), "other/account"); !got.IsZero() {
		t.Fatalf("another account must not be refreshed by a filtered run: %v", got)
	}
}

func TestEverySourceFailingIsAFailedRun(t *testing.T) {
	root, _ := twoSourceRoot(t)
	db := newDB(t)
	hookFlush(t, 2000, func(string, int) error { return errs.StoreMissing("injected") })
	rep, changes, err := Run(context.Background(), Options{Root: root, DBPath: db})
	codedErr(t, err, errs.CodeStoreMissing)
	if rep.Status != "failed" || len(changes) != 0 || len(rep.Sources) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	st := readStatus(t, db)
	if st.LastRun == nil || st.LastRun.Status != "failed" || !st.LastSuccessAt.IsZero() {
		t.Fatalf("last run: %+v", st.LastRun)
	}
}

func TestInterruptedRunReportsInterruptedAndKeepsCommittedChanges(t *testing.T) {
	root, _ := twoSourceRoot(t)
	db := newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := 0
	old := afterSnapshot
	afterSnapshot = func() {
		if seen++; seen == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { afterSnapshot = old })
	rep, changes, err := Run(ctx, Options{Root: root, DBPath: db})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if rep.Status != StatusPartial || len(changes) != fixtureMessages+fixtureActivity {
		t.Fatalf("report %q, %d changes", rep.Status, len(changes))
	}
	if st := readStatus(t, db); !st.LastSuccessAt.IsZero() || st.LastRun == nil || st.LastRun.Status != StatusPartial {
		t.Fatalf("an interrupted run refreshes nobody: %+v", st)
	}
}

func TestCancelledDuringTheSnapshotLoadIsInterrupted(t *testing.T) {
	// A source whose files vanish because cancelling removed the snapshot reads as a missing file;
	// the run must still report the cancellation.
	isolateTmp(t)
	db := newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := afterSnapshot
	afterSnapshot = func() {
		cancel()
		// Make the read fail the way a removed snapshot does.
		for _, d := range snapshotDirs(t, os.TempDir()) {
			_ = os.Remove(filepath.Join(d, "leveldb", "CURRENT"))
		}
	}
	t.Cleanup(func() { afterSnapshot = old })
	_, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: db})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if st := readStatus(t, db); st.LastRun == nil || st.LastRun.Status != "failed" {
		t.Fatalf("last run: %+v", st.LastRun)
	}
}

func TestRunLevelRecordFailureIsNotAFreshSync(t *testing.T) {
	isolateTmp(t)
	db := archiveWith(t, `create trigger boom before insert on sync_runs when new.accounts_json is not null and new.status in ('ok','ok_with_omissions') begin select raise(abort, 'injected'); end;`)
	rep, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	codedErr(t, err, errs.CodeDBError)
	if rep.Status != StatusOK {
		t.Fatalf("the sources did commit: %+v", rep)
	}
	if st := readStatus(t, db); !st.LastSuccessAt.IsZero() {
		t.Fatalf("an unrecorded run is not a fresh sync: %v", st.LastSuccessAt)
	}
}

func TestStubbornPauseIgnoresCancellation(t *testing.T) {
	isolateTmp(t)
	t.Setenv(testPauseEnv, "30ms")
	t.Setenv(testPauseStubbornEnv, "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := afterSnapshot
	afterSnapshot = cancel // cancelled right after the pause: the pause itself did not stop early
	t.Cleanup(func() { afterSnapshot = old })
	start := time.Now()
	_, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: newDB(t)})
	if !errors.Is(err, context.Canceled) || time.Since(start) < 30*time.Millisecond {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestPanicsInRederiveAndDiscoveryAreContained(t *testing.T) {
	isolateTmp(t)
	oldR, oldD := rederiveArchive, discoverSources
	t.Cleanup(func() { rederiveArchive, discoverSources = oldR, oldD })
	rederiveArchive = func(context.Context, *store.Store) (*store.Migration, error) { panic("boom in rederive") }
	db := newDB(t)
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	codedErr(t, err, errs.CodeDBError) // an error from Rederive is a database error
	if !strings.Contains(err.Error(), "boom in rederive") {
		t.Fatalf("err = %v", err)
	}
	rederiveArchive = oldR
	discoverSources = func(string) ([]teamsdesktop.Source, []string, error) { panic("boom in discover") }
	_, _, err = Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	codedErr(t, err, errs.CodeInternal)
	if !strings.Contains(err.Error(), "boom in discover") {
		t.Fatalf("err = %v", err)
	}
	discoverSources = oldD
	if r, _ := run(t, Options{Root: fixtureRoot, DBPath: db}); r.Status != StatusOK {
		t.Fatalf("the lock was released and a clean run works: %+v", r)
	}
}
