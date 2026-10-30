package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// countSyncs counts the implicit syncs a read starts, still running each one. The returned
// channel closes when the first one has started, so a test can let the lock go only once the read
// has met it.
func countSyncs(t *testing.T) (*atomic.Int32, <-chan struct{}) {
	t.Helper()
	var n atomic.Int32
	started := make(chan struct{})
	old := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		if n.Add(1) == 1 {
			defer close(started)
		}
		return old(ctx, o)
	}
	t.Cleanup(func() { runSync = old })
	return &n, started
}

func shortLockWait(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldPoll := lockWait, lockPoll
	lockWait, lockPoll = wait, 10*time.Millisecond
	t.Cleanup(func() { lockWait, lockPoll = oldWait, oldPoll })
}

// holdLock plays another m365crawl run: it holds the archive lock and lets it go hold after start
// closes (nil: at once). With synced, that run records a complete sync just before it lets go, as a
// successful sync does.
func holdLock(t *testing.T, db string, start <-chan struct{}, hold time.Duration, synced bool) {
	t.Helper()
	release, err := store.AcquireLock(db)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer release()
		if start != nil {
			select {
			case <-start:
			case <-time.After(time.Minute):
			}
		}
		time.Sleep(hold)
		if !synced {
			return
		}
		st, err := store.Open(context.Background(), db)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = st.Close() }()
		now := time.Now().UTC()
		if err := st.RecordRun(context.Background(), store.Run{StartedAt: now.Add(-hold), FinishedAt: now, Status: syncer.StatusOK, Accounts: []string{"*"}}); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { <-done })
}

func TestReadWaitsForAnotherRunsSyncInsteadOfFailingLocked(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 10*time.Second)
	syncs, started := countSyncs(t)
	holdLock(t, e.db, started, 300*time.Millisecond, true)
	began := time.Now()
	code, stdout, stderr := e.run("--max-age", "1ns", "search", "Fixture", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if time.Since(began) < 300*time.Millisecond {
		t.Fatal("the read did not wait for the other run")
	}
	m := decode(t, stdout)
	if m["sync_error"] != nil || strings.Contains(stderr, `"locked"`) {
		t.Fatalf("a read beside another run's sync must not report locked: sync_error %v, stderr %s", m["sync_error"], stderr)
	}
	if m["implicit_sync"] != implicitSyncWaited {
		t.Fatalf("implicit_sync = %v, want %s", m["implicit_sync"], implicitSyncWaited)
	}
	if m["synced"] != nil {
		t.Fatalf("this read ran no sync of its own, yet synced = %v", m["synced"])
	}
	if n := syncs.Load(); n != 1 {
		t.Fatalf("the read started %d syncs; after the other run's sync it must not sync again", n)
	}
	if len(items(t, m)) == 0 {
		t.Fatal("expected hits")
	}
	if !strings.Contains(stderr, `"notice":"waiting_for_other_sync"`) {
		t.Fatalf("no waiting notice on stderr: %s", stderr)
	}
}

func TestReadSyncsItselfWhenTheOtherRunLeftTheArchiveStale(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 10*time.Second)
	syncs, started := countSyncs(t)
	holdLock(t, e.db, started, 200*time.Millisecond, false) // a fetch, or a sync that failed
	code, stdout, stderr := e.run("--max-age", "1ns", "search", "Fixture", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["sync_error"] != nil || m["implicit_sync"] != nil {
		t.Fatalf("sync_error %v, implicit_sync %v", m["sync_error"], m["implicit_sync"])
	}
	synced, _ := m["synced"].(map[string]any)
	if synced == nil || synced["status"] == syncer.StatusFailed {
		t.Fatalf("synced = %v, want this read's own sync", m["synced"])
	}
	if n := syncs.Load(); n != 2 {
		t.Fatalf("syncs = %d, want the locked attempt and this read's own sync", n)
	}
}

func TestReadAnswersFromTheArchiveWhenAnotherRunOutlastsTheWait(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 300*time.Millisecond)
	holdLock(t, e.db, nil, 5*time.Second, false)
	began := time.Now()
	code, stdout, stderr := e.run("--max-age", "1ns", "search", "Fixture", "--json")
	if code != 0 {
		t.Fatalf("read must succeed on the existing archive: exit %d, %s", code, stderr)
	}
	if took := time.Since(began); took < 300*time.Millisecond || took > 4*time.Second {
		t.Fatalf("the read took %s; it should give up after its 300ms budget, long before the other run ends", took)
	}
	m := decode(t, stdout)
	if m["sync_error"] != nil || strings.Contains(stderr, `"warning"`) {
		t.Fatalf("waiting out another run is not a failure: sync_error %v, stderr %s", m["sync_error"], stderr)
	}
	if m["implicit_sync"] != implicitSyncOtherInProgress {
		t.Fatalf("implicit_sync = %v, want %s", m["implicit_sync"], implicitSyncOtherInProgress)
	}
	notices, _ := m["notices"].([]any)
	if len(notices) == 0 || !strings.Contains(notices[0].(string), "still syncing") {
		t.Fatalf("notices = %v", notices)
	}
	if m["archive_age_seconds"] == nil || len(items(t, m)) == 0 {
		t.Fatalf("expected hits from the existing archive and its age: %v", m)
	}
}

func TestReadWaitingForAnotherRunSaysSoInText(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 200*time.Millisecond)
	holdLock(t, e.db, nil, 5*time.Second, false)
	code, stdout, stderr := e.run("--max-age", "1ns", "--format", "text", "search", "Fixture")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "m365crawl: waiting for another m365crawl run to finish with the archive (up to ") {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout+stderr, "notice: Another m365crawl run was still syncing") {
		t.Fatalf("no notice in text output:\nstdout %q\nstderr %q", stdout, stderr)
	}
}

func TestReadWaitingForAnotherRunStopsOnCancel(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 10*time.Second)
	holdLock(t, e.db, nil, 5*time.Second, false)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	var out, errb bytes.Buffer
	began := time.Now()
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--max-age", "1ns", "search", "Fixture"}, &out, &errb)
	if code != errs.ExitRuntime || errorOf(t, errb.String())["code"] != errs.CodeInterrupted {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if time.Since(began) > 4*time.Second {
		t.Fatal("cancellation did not end the wait")
	}
}

func TestSourceRefreshWaitsForAnotherRun(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 10*time.Second)
	first := make(chan struct{})
	holdLock(t, e.db, first, 200*time.Millisecond, false)
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	rt.dbPath = e.db
	calls := 0
	err := rt.besideOtherRun(func() error {
		if calls++; calls == 1 {
			close(first)
		}
		release, err := store.AcquireLock(e.db)
		if err != nil {
			return err
		}
		release()
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls; the refresh should run again once the other run let go", err, calls)
	}
}

func TestSourceRefreshGivesUpAfterTheWait(t *testing.T) {
	e := newEnv(t)
	e.sync()
	shortLockWait(t, 100*time.Millisecond)
	holdLock(t, e.db, nil, 5*time.Second, false)
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	rt.dbPath = e.db
	err := rt.besideOtherRun(func() error { return errs.Locked("held") })
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeLocked || !strings.Contains(coded.Message, "waited") {
		t.Fatalf("err = %v, want locked saying how long it waited", err)
	}
}

func TestReadWaitBudgetIsSharedByOneRead(t *testing.T) {
	shortLockWait(t, 200*time.Millisecond)
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	first := rt.lockDeadline()
	time.Sleep(20 * time.Millisecond)
	if rt.lockDeadline() != first {
		t.Fatal("a second wait in the same read got a fresh budget")
	}
}

func TestWaitText(t *testing.T) {
	if got := waitText(90 * time.Second); got != "1m30s" {
		t.Fatalf("waitText(90s) = %q", got)
	}
	if got := waitText(300 * time.Millisecond); got != "300ms" {
		t.Fatalf("waitText(300ms) = %q", got)
	}
}

func TestLockWaitTestEnvSetsTheBudget(t *testing.T) {
	t.Setenv(lockWaitTestEnv, "3s")
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	if left := time.Until(rt.lockDeadline()); left <= 2*time.Second || left > 3*time.Second || rt.lockBudget != 3*time.Second {
		t.Fatalf("budget %s, left %s", rt.lockBudget, left)
	}
}

// unlockableRuntime is a read with no archive yet whose lock file cannot be opened: it is a directory.
func unlockableRuntime(t *testing.T, ctx context.Context) *runtime {
	t.Helper()
	shortLockWait(t, 10*time.Second)
	db := filepath.Join(t.TempDir(), "m365crawl.db")
	if err := os.Mkdir(db+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(ctx, &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	rt.maxAge, rt.dbPath = defaultMaxAge, db
	return rt
}

func TestReadWaitStopsOnAnArchiveFailure(t *testing.T) {
	old := runSync
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errs.Locked("held")
	}
	t.Cleanup(func() { runSync = old })
	rt := unlockableRuntime(t, context.Background())
	_, err := rt.ensureFresh()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError {
		t.Fatalf("err = %v, want db_error", err)
	}
}

func TestSourceRefreshWaitStopsOnAnArchiveFailure(t *testing.T) {
	rt := unlockableRuntime(t, context.Background())
	err := rt.besideOtherRun(func() error { return errs.Locked("held") })
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError {
		t.Fatalf("err = %v, want db_error", err)
	}
}

func TestSourceRefreshWaitStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := unlockableRuntime(t, ctx)
	if err := rt.besideOtherRun(func() error { return errs.Locked("held") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
