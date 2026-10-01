package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// --- file events ---

func TestFileEventsNeedAtLeastOneWatchableDirectory(t *testing.T) {
	ctx := context.Background()
	if _, _, err := fileEvents(ctx, []string{"", ""}); err == nil || !strings.Contains(err.Error(), "no directories to watch") {
		t.Fatalf("empty dirs: err = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := fileEvents(ctx, []string{missing}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing dir: err = %v, want not-exist", err)
	}
	// One good directory is enough; the bad one is skipped.
	good := t.TempDir()
	events, closeEvents, err := fileEvents(ctx, []string{missing, "", good})
	if err != nil {
		t.Fatalf("one watchable directory must be enough: %v", err)
	}
	defer closeEvents()
	if err := os.WriteFile(filepath.Join(good, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("a write in the watched directory produced no event")
	}
}

func TestFileEventsCloseIsIdempotent(t *testing.T) {
	_, closeEvents, err := fileEvents(context.Background(), []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	closeEvents()
	closeEvents() // a second call must not panic on the closed stop channel
}

func TestFileEventsReportsWatcherCreationFailure(t *testing.T) {
	old := newFSWatcher
	newFSWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("too many open files") }
	t.Cleanup(func() { newFSWatcher = old })
	if _, _, err := fileEvents(context.Background(), []string{t.TempDir()}); err == nil || !strings.Contains(err.Error(), "too many open files") {
		t.Fatalf("err = %v", err)
	}
}

func finishes(fn func()) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestPumpEventsStopsWhenAnyInputEnds(t *testing.T) {
	ev := make(chan fsnotify.Event)
	errc := make(chan error)
	out := make(chan struct{}, 1)
	cases := map[string]func() (context.Context, <-chan struct{}, <-chan fsnotify.Event, <-chan error){
		"context cancelled": func() (context.Context, <-chan struct{}, <-chan fsnotify.Event, <-chan error) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, make(chan struct{}), ev, errc
		},
		"stop closed": func() (context.Context, <-chan struct{}, <-chan fsnotify.Event, <-chan error) {
			stop := make(chan struct{})
			close(stop)
			return context.Background(), stop, ev, errc
		},
		"events closed": func() (context.Context, <-chan struct{}, <-chan fsnotify.Event, <-chan error) {
			c := make(chan fsnotify.Event)
			close(c)
			return context.Background(), make(chan struct{}), c, errc
		},
		"errors closed": func() (context.Context, <-chan struct{}, <-chan fsnotify.Event, <-chan error) {
			c := make(chan error)
			close(c)
			return context.Background(), make(chan struct{}), ev, c
		},
	}
	for name, setup := range cases {
		ctx, stop, events, errors := setup()
		if !finishes(func() { pumpEvents(ctx, stop, events, errors, out) }) {
			t.Errorf("%s: pumpEvents did not return", name)
		}
	}
}

func TestPumpEventsForwardsChangesCoalescesThemAndIgnoresChmodAndErrors(t *testing.T) {
	ev := make(chan fsnotify.Event)
	errc := make(chan error)
	out := make(chan struct{}, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { pumpEvents(context.Background(), stop, ev, errc, out); close(done) }()

	ev <- fsnotify.Event{Name: "a", Op: fsnotify.Chmod}
	errc <- errors.New("queue overflow")
	select {
	case <-out:
		t.Fatal("a chmod event or an error produced a signal")
	default:
	}
	ev <- fsnotify.Event{Name: "a", Op: fsnotify.Write}
	ev <- fsnotify.Event{Name: "b", Op: fsnotify.Create} // dropped: one signal is already pending
	close(stop)
	<-done
	if len(out) != 1 {
		t.Fatalf("pending signals = %d, want exactly 1", len(out))
	}
}

// --- the watcher's pieces ---

func jsonWatcher(t *testing.T, root, db string) (*watcher, *syncBuf, *syncBuf) {
	t.Helper()
	var out, errb syncBuf
	rt := newRuntime(context.Background(), &Globals{}, &out, &errb)
	rt.format, rt.root, rt.dbPath = output.JSON, root, db
	return &watcher{rt: rt, every: 30 * time.Millisecond}, &out, &errb
}

func TestWatcherRootDirDefaultsToTheTeamsContainer(t *testing.T) {
	w, _, _ := jsonWatcher(t, "", "x.db")
	if got := w.rootDir(); got != teamsdesktop.DefaultRoot() {
		t.Fatalf("rootDir = %q, want %q", got, teamsdesktop.DefaultRoot())
	}
	w.rt.root = "/some/root"
	if got := w.rootDir(); got != "/some/root" {
		t.Fatalf("rootDir = %q", got)
	}
}

func TestSameFingerprintsComparesSizesAndValues(t *testing.T) {
	a := map[string]string{"x": "1", "y": "2"}
	for name, c := range map[string]struct {
		b    map[string]string
		same bool
	}{
		"equal":        {map[string]string{"y": "2", "x": "1"}, true},
		"fewer":        {map[string]string{"x": "1"}, false},
		"more":         {map[string]string{"x": "1", "y": "2", "z": "3"}, false},
		"other value":  {map[string]string{"x": "1", "y": "9"}, false},
		"other source": {map[string]string{"x": "1", "z": "2"}, false},
	} {
		if got := sameFingerprints(a, c.b); got != c.same {
			t.Errorf("%s: sameFingerprints = %v, want %v", name, got, c.same)
		}
	}
}

func TestWatcherFingerprintingReportsDiscoveryAndReadFailures(t *testing.T) {
	e := newEnv(t)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	fps, err := w.fingerprints()
	if err != nil || len(fps) == 0 {
		t.Fatalf("fingerprints = %v, %v", fps, err)
	}
	if changed, err := w.changed(); err != nil || !changed {
		t.Fatalf("an unbaselined watcher sees a change: %v, %v", changed, err)
	}
	w.fps = fps
	if changed, err := w.changed(); err != nil || changed {
		t.Fatalf("same fingerprints: changed = %v, err = %v", changed, err)
	}

	oldD, oldF := discover, fingerprintOf
	t.Cleanup(func() { discover, fingerprintOf = oldD, oldF })
	fingerprintOf = func(teamsdesktop.Source) (string, error) { return "", errors.New("unreadable") }
	if _, err := w.fingerprints(); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("fingerprint failure: err = %v", err)
	}
	if _, err := w.changed(); err == nil {
		t.Error("changed must pass the fingerprint failure on")
	}
	if _, err := w.sync(); err == nil {
		t.Error("sync must pass the fingerprint failure on")
	}
	discover = func(string) ([]teamsdesktop.Source, []string, error) { return nil, nil, errs.TeamsNotInstalled("/x") }
	if _, err := w.fingerprints(); err == nil {
		t.Error("discovery failure must be returned")
	}
}

func TestWatcherSyncSkipsWhenTheCacheIsUnchangedSinceTheBaseline(t *testing.T) {
	e := newEnv(t)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	fps, err := w.fingerprints()
	if err != nil {
		t.Fatal(err)
	}
	w.baselined, w.fps = true, fps
	var calls atomic.Int32
	old := runSync
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls.Add(1)
		return syncer.Report{}, nil, nil
	}
	t.Cleanup(func() { runSync = old })
	done, err := w.sync()
	if !done || err != nil || calls.Load() != 0 {
		t.Fatalf("done = %v, err = %v, sync calls = %d; want done without syncing", done, err, calls.Load())
	}
}

func TestWatcherReportRules(t *testing.T) {
	e := newEnv(t)
	w, out, errb := jsonWatcher(t, e.root, e.db)

	// An uncoded failure becomes one internal error line, and an identical repeat is silent.
	if err := w.report(errors.New("flaky disk")); err != nil {
		t.Fatalf("report = %v, want the watch to continue", err)
	}
	_ = w.report(errors.New("flaky disk"))
	lines := kinds(out.lines(t), "error")
	if len(lines) != 1 || lines[0]["error"].(map[string]any)["code"] != errs.CodeInternal {
		t.Fatalf("error lines = %v, want exactly one internal error", lines)
	}
	if !w.baselineFailed {
		t.Error("a failure before the baseline must be remembered")
	}

	// A held lock is only a warning on stderr.
	if err := w.report(errs.Locked("held")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), `"warning"`) || !strings.Contains(errb.String(), "locked") {
		t.Fatalf("stderr = %q, want a locked warning", errb.String())
	}

	// An environment failure ends the watch.
	env := errs.TeamsNotInstalled("/x")
	if err := w.report(env); !errors.Is(err, env) {
		t.Fatalf("report(env) = %v, want the coded error back", err)
	}
	if err := w.reportErr(env); !errors.Is(err, env) {
		t.Fatalf("reportErr(env) = %v, want the coded error back", err)
	}
	if err := w.reportErr(errors.New("transient")); err != nil {
		t.Fatalf("reportErr(transient) = %v, want nil", err)
	}

	// In text mode errors go to stderr as text.
	w.rt.format = output.Text
	errb2 := &syncBuf{}
	w.rt.stderr = errb2
	_ = w.report(errs.SnapshotInconsistent("moving"))
	if !strings.HasPrefix(errb2.String(), "error: ") {
		t.Fatalf("text error = %q", errb2.String())
	}
}

func TestWatcherReportIgnoresFailuresAfterCancellation(t *testing.T) {
	e := newEnv(t)
	w, out, errb := jsonWatcher(t, e.root, e.db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.rt.ctx = ctx
	if err := w.report(errs.TeamsNotInstalled("/x")); err != nil {
		t.Fatalf("report after cancel = %v, want nil", err)
	}
	if out.String() != "" || errb.String() != "" {
		t.Fatalf("nothing may be printed after cancel: %q %q", out.String(), errb.String())
	}
}

func TestWatcherItemsReportsUnreadableRows(t *testing.T) {
	e := newEnv(t)
	e.sync()
	w, _, _ := jsonWatcher(t, e.root, e.db)
	changes := []syncer.Change{{Kind: "message", Change: "new", Key: "t|u|c|1"}, {Kind: "activity", Change: "new", Key: "t|u|1"}}
	if got, err := w.items(nil); err != nil || len(got) != 0 {
		t.Fatalf("no changes: %v, %v", got, err)
	}
	e.exec("drop table activity")
	var c *errs.Coded
	_, err := w.items(changes[1:]) // activity only: the message lookup is skipped
	if !errors.As(err, &c) || c.Code != errs.CodeDBError {
		t.Fatalf("activity failure: err = %v, want db_error", err)
	}
	e.exec("drop table messages")
	_, err = w.items(changes)
	if !errors.As(err, &c) || c.Code != errs.CodeDBError {
		t.Fatalf("message failure: err = %v, want db_error", err)
	}
	if _, err := w.items([]syncer.Change{{Kind: "message", Key: "t|u|c|1"}}); !errors.As(err, &c) {
		t.Fatalf("a missing archive table must fail the lookup: %v", err)
	}
}

func TestWatcherItemsFailsWhenTheArchiveCannotBeOpened(t *testing.T) {
	e := newEnv(t)
	e.garbageArchive()
	w, _, _ := jsonWatcher(t, e.root, e.db)
	_, err := w.items([]syncer.Change{{Kind: "message", Key: "t|u|c|1"}})
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeDBError {
		t.Fatalf("err = %v, want db_error", err)
	}
}

func TestWatcherTextLineNamesTheActivityType(t *testing.T) {
	e := newEnv(t)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	var out bytes.Buffer
	w.rt.stdout = &out
	w.textLine(syncer.Change{Kind: "activity", Change: "new"}, watchItem{label: "mentionInChat", conv: "Chat", sender: "Ann", text: "hello\nthere"})
	got := out.String()
	if !strings.Contains(got, " new activity mentionInChat  Chat | Ann: hello there\n") {
		t.Fatalf("line = %q", got)
	}
	out.Reset()
	w.textLine(syncer.Change{Kind: "message", Change: "edited"}, watchItem{conv: "Chat", sender: "Ann", text: "x"})
	if !strings.Contains(out.String(), " edited message  Chat | Ann: x\n") {
		t.Fatalf("line = %q", out.String())
	}
}

// --- the loop ---

func runWatcher(t *testing.T, w *watcher) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w.rt.ctx = ctx
	res := make(chan error, 1)
	go func() { res <- w.run() }()
	t.Cleanup(cancel)
	return func() error {
		cancel()
		select {
		case err := <-res:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("watcher did not stop")
			return nil
		}
	}
}

func TestWatchWarnsAndPollsWhenFileEventsAreUnavailable(t *testing.T) {
	fastWatch(t)
	old := watchEvents
	watchEvents = func(context.Context, []string) (<-chan struct{}, func(), error) {
		return nil, nil, errors.New("inotify limit reached")
	}
	t.Cleanup(func() { watchEvents = old })
	e := newEnv(t)
	w, _, errb := jsonWatcher(t, e.root, e.db)
	stop := runWatcher(t, w)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(errb.String(), "file events unavailable, polling every 30ms") {
		if time.Now().After(deadline) {
			t.Fatalf("no polling warning: %q", errb.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("run = %v, want nil on cancel", err)
	}
}

// brokenAfterBaseline lets the baseline sync through, then makes fingerprinting fail with err.
func brokenAfterBaseline(t *testing.T, w *watcher, err error) {
	t.Helper()
	noEvents(t)
	fastWatch(t)
	oldF := fingerprintOf
	var broken atomic.Bool
	fingerprintOf = func(s teamsdesktop.Source) (string, error) {
		if broken.Load() {
			return "", err
		}
		return oldF(s)
	}
	t.Cleanup(func() { fingerprintOf = oldF })
	quit := make(chan struct{})
	t.Cleanup(func() { close(quit) })
	go func() {
		for !archiveFilled(w.rt.dbPath) {
			select {
			case <-quit:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
		broken.Store(true)
	}()
}

// archiveFilled reports whether the baseline sync has put messages in the archive.
func archiveFilled(db string) bool {
	return (&watchEnv{env: &env{db: db}}).archiveCount("select count(*) from messages") > 0
}

func TestWatchEndsWithTheEnvironmentErrorWhenPollingFindsTeamsGone(t *testing.T) {
	e := newEnv(t)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	brokenAfterBaseline(t, w, errs.NoFullDiskAccess("/x", errors.New("denied")))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w.rt.ctx = ctx
	err := w.run()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeNoFullDiskAccess {
		t.Fatalf("run = %v, want no_full_disk_access", err)
	}
}

func TestWatchKeepsPollingAfterATransientPollFailure(t *testing.T) {
	e := newEnv(t)
	w, out, _ := jsonWatcher(t, e.root, e.db)
	brokenAfterBaseline(t, w, errors.New("transient read failure"))
	stop := runWatcher(t, w)
	deadline := time.Now().Add(10 * time.Second)
	for len(kinds(out.lines(t), "error")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no error line: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // several more polls: the same failure is reported once
	if err := stop(); err != nil {
		t.Fatalf("run = %v, want nil: a transient failure must not end the watch", err)
	}
	if n := len(kinds(out.lines(t), "error")); n != 1 {
		t.Fatalf("error lines = %d, want 1", n)
	}
}

func TestWatchEndsWithTheEnvironmentErrorWhenASyncFindsTeamsGone(t *testing.T) {
	fastWatch(t)
	noEvents(t)
	e := newEnv(t)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	oldF := fingerprintOf
	fingerprintOf = func(teamsdesktop.Source) (string, error) {
		return "", errs.NoFullDiskAccess("/x", errors.New("denied"))
	}
	t.Cleanup(func() { fingerprintOf = oldF })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w.rt.ctx = ctx
	err := w.run()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeNoFullDiskAccess {
		t.Fatalf("run = %v, want no_full_disk_access", err)
	}
}
