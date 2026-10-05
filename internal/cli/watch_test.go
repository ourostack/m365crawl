package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// syncBuf is a bytes.Buffer the watch goroutine and the test can use at once.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines parses the buffer as JSON Lines; a non-JSON line fails the test.
func (s *syncBuf) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(s.String(), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not a JSON line: %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func kinds(ls []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, l := range ls {
		if l["kind"] == kind {
			out = append(out, l)
		}
	}
	return out
}

// watchEnv is a watch run against a private copy of the fixture cache.
type watchEnv struct {
	*env
	out, errb *syncBuf
	cancel    context.CancelFunc
	done      chan int
	fin       chan struct{} // closed when the run has returned
	touches   int
}

// copyTree copies src into dst (regular files and directories only).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: copies the repository's own fixture
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600) //nolint:gosec // G703: dst is a test temp dir
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fastWatch shrinks the debounce so tests run in milliseconds.
func fastWatch(t *testing.T) {
	t.Helper()
	q, m, l := watchQuiet, watchMaxWait, watchLockedRetry
	watchQuiet, watchMaxWait, watchLockedRetry = 20*time.Millisecond, 10*time.Second, 30*time.Millisecond
	t.Cleanup(func() { watchQuiet, watchMaxWait, watchLockedRetry = q, m, l })
}

// noEvents makes watch poll only.
func noEvents(t *testing.T) {
	t.Helper()
	old := watchEvents
	watchEvents = func(context.Context, []string) (<-chan struct{}, func(), error) { return nil, func() {}, nil }
	t.Cleanup(func() { watchEvents = old })
}

func newWatchEnv(t *testing.T) *watchEnv {
	t.Helper()
	fastWatch(t)
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "EBWebView")
	copyTree(t, e.root, root)
	e.root = root
	return &watchEnv{env: e, out: &syncBuf{}, errb: &syncBuf{}}
}

func (w *watchEnv) start(args ...string) {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan int, 1)
	w.fin = make(chan struct{})
	full := append([]string{"--db", w.db, "--teams-root", w.root}, args...)
	if !contains(args, "--format") {
		full = append(full, "--json")
	}
	if !contains(args, "--min-interval") { // the 60 s default would stall every test that syncs twice
		full = append(full, "--min-interval", "40ms")
	}
	go func() { w.done <- runCLI(ctx, full, w.out, w.errb); close(w.fin) }()
	w.t.Cleanup(func() {
		cancel()
		select {
		case <-w.fin:
		case <-time.After(10 * time.Second):
		}
	})
}

// stop cancels the run (what SIGINT/SIGTERM do through Main) and returns its exit code.
func (w *watchEnv) stop() int {
	w.t.Helper()
	w.cancel()
	select {
	case code := <-w.done:
		return code
	case <-time.After(10 * time.Second):
		w.t.Fatal("watch did not stop")
		return -1
	}
}

func (w *watchEnv) waitFor(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		select {
		case code := <-w.done:
			w.t.Fatalf("watch exited (%d) while waiting for %s\nstdout: %s\nstderr: %s", code, what, w.out.String(), w.errb.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	w.t.Fatalf("timed out waiting for %s\nstdout: %s\nstderr: %s", what, w.out.String(), w.errb.String())
}

// baselineDone waits until the first sync has filled the archive.
func (w *watchEnv) baselineDone() {
	w.t.Helper()
	w.waitFor("the baseline sync", func() bool { return w.archiveCount("select count(*) from messages") > 0 })
}

func (w *watchEnv) archiveCount(q string) int {
	db, err := sql.Open("sqlite", w.db+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0
	}
	defer func() { _ = db.Close() }()
	var n int
	if db.QueryRow(q).Scan(&n) != nil {
		return 0
	}
	return n
}

func (w *watchEnv) exec(q string, args ...any) {
	w.t.Helper()
	db, err := sql.Open("sqlite", w.db+"?_pragma=busy_timeout(5000)")
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(q, args...); err != nil {
		w.t.Fatal(err)
	}
}

// forget removes the messages matching cond (a where clause on messages) from the archive, so the
// next sync inserts them again and reports them as new.
func (w *watchEnv) forget(cond string) {
	w.t.Helper()
	w.exec(`delete from message_fts where rowid in (select rowid from messages where ` + cond + `)`)
	w.exec(`delete from messages where ` + cond)
}

func (w *watchEnv) sources() []teamsdesktop.Source {
	w.t.Helper()
	srcs, _, err := teamsdesktop.Discover(w.root)
	if err != nil || len(srcs) == 0 {
		w.t.Fatalf("discover: %v", err)
	}
	return srcs
}

// touch changes the mtime of one cache file, which changes the source's fingerprint.
func (w *watchEnv) touch() {
	w.t.Helper()
	dir := w.sources()[0].LevelDBDir
	ents, err := os.ReadDir(dir)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || e.Name() == "LOCK" || strings.HasPrefix(e.Name(), "LOG") {
			continue
		}
		w.touches++
		ts := time.Now().Add(time.Duration(w.touches) * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, e.Name()), ts, ts); err != nil {
			w.t.Fatal(err)
		}
		return
	}
	w.t.Fatal("no file to touch")
}

func TestWatchIdle(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	time.Sleep(400 * time.Millisecond) // many polls with nothing changed
	if got := w.out.String(); got != "" {
		t.Fatalf("an idle watch must print nothing (baseline is silent), got %q", got)
	}
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errb.String())
	}
}

func TestWatchSyncsOnChange(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	// Make the next sync see one message as new and another as edited.
	w.forget(`rowid=(select min(rowid) from messages)`)
	w.exec(`update messages set version=0, content_hash='', content_text='stale' where rowid=(select max(rowid) from messages)`)
	w.touch()
	w.waitFor("the sync line", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	time.Sleep(300 * time.Millisecond) // polling continues; the unchanged cache must not sync again
	ls := w.out.lines(t)
	if n := len(kinds(ls, "sync")); n != 1 {
		t.Fatalf("want exactly one sync line, got %d:\n%s", n, w.out.String())
	}
	rep, _ := kinds(ls, "sync")[0]["report"].(map[string]any)
	if rep["status"] == nil || rep["messages"] == nil {
		t.Fatalf("sync line has no report: %v", ls)
	}
	changes := map[string]int{}
	for _, l := range kinds(ls, "message") {
		changes[l["change"].(string)]++
		it, _ := l["item"].(map[string]any)
		if it["id"] == nil || it["conversation_id"] == nil || it["conversation_display_name"] == nil {
			t.Fatalf("item has the messages shape: %v", l)
		}
	}
	if changes["new"] != 1 || changes["edited"] != 1 {
		t.Fatalf("changes = %v\n%s", changes, w.out.String())
	}
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestWatchFieldsMaxTextAndAccount(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms", "--fields", "id,text", "--max-text", "5")
	w.baselineDone()
	w.forget(`content_text<>''`)
	w.touch()
	w.waitFor("a change", func() bool { return len(kinds(w.out.lines(t), "message")) > 0 })
	l := kinds(w.out.lines(t), "message")[0]
	it := l["item"].(map[string]any)
	for k := range it {
		if k != "id" && k != "text" && k != "text_truncated" {
			t.Errorf("--fields leaked %q", k)
		}
	}
	if txt, _ := it["text"].(string); len([]rune(txt)) > 6 { // 5 characters and an ellipsis
		t.Errorf("--max-text not applied: %q", txt)
	}
	if l["kind"] != "message" || l["change"] != "new" {
		t.Errorf("envelope: %v", l)
	}
}

func TestWatchFieldsExplicitTextTruncated(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms", "--fields", "id,text_truncated", "--max-text", "5")
	w.baselineDone()
	w.forget(`content_text<>''`)
	w.touch()
	w.waitFor("a change", func() bool { return len(kinds(w.out.lines(t), "message")) > 0 })
	l := kinds(w.out.lines(t), "message")[0]
	it := l["item"].(map[string]any)
	if len(it) != 2 || it["id"] == nil || it["text_truncated"] != true {
		t.Fatalf("explicit text_truncated field: %v", it)
	}
}

func TestWatchAccountFilter(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms", "--account", "no-such-tenant/no-such-user")
	w.baselineDone()
	w.forget(`1=1`)
	w.touch()
	w.waitFor("the sync line", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	if n := len(kinds(w.out.lines(t), "message")); n != 0 {
		t.Fatalf("another account's changes were emitted: %s", w.out.String())
	}
}

func TestWatchEmitInitial(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "1h", "--emit-initial")
	w.waitFor("the initial changes", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	ls := w.out.lines(t)
	if len(kinds(ls, "message")) == 0 {
		t.Fatalf("--emit-initial must report the baseline's messages as new: %s", w.out.String())
	}
	for _, l := range kinds(ls, "message") {
		if l["change"] != "new" {
			t.Fatalf("baseline change = %v", l["change"])
		}
	}
}

func TestWatchTextMode(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms", "--format", "text")
	w.baselineDone()
	w.forget(`rowid=(select min(rowid) from messages)`)
	w.touch()
	w.waitFor("a text line", func() bool { return strings.Contains(w.out.String(), "new") })
	for _, l := range strings.Split(strings.TrimSpace(w.out.String()), "\n") {
		if strings.HasPrefix(l, "{") {
			t.Fatalf("text mode printed JSON: %q", l)
		}
	}
	if !strings.Contains(w.out.String(), "message") {
		t.Fatalf("text line names no kind: %q", w.out.String())
	}
}

func TestWatchSignal(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errb.String())
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "teamscrawl-snapshot-*"))
	if len(left) != 0 {
		t.Fatalf("snapshot directories left behind: %v", left)
	}
}

// A cancellation that lands while a sync is running (here: held after the snapshot) still exits 0
// and removes the snapshot.
func TestWatchSignalDuringSync(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	t.Setenv("TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT", "30s")
	w.start("watch", "--every", "1h")
	w.waitFor("the snapshot", func() bool {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "teamscrawl-snapshot-*"))
		return len(m) > 0
	})
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errb.String())
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "teamscrawl-snapshot-*"))
	if len(left) != 0 {
		t.Fatalf("snapshot directories left behind: %v", left)
	}
}

func TestWatchLockedSkips(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	release, err := store.AcquireLock(w.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w.start("watch", "--every", "30ms")
	w.waitFor("the locked warning", func() bool { return strings.Contains(w.errb.String(), "locked") })
	select {
	case code := <-w.done:
		t.Fatalf("watch exited (%d) on a locked archive instead of waiting", code)
	default:
	}
	if strings.Contains(w.errb.String(), `"error"`) {
		t.Fatalf("locked is a warning, not an error: %s", w.errb.String())
	}
	release() // the other run finishes; the next poll syncs
	w.baselineDone()
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestWatchErrorLineContinues(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	var calls atomic.Int32
	old := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		if calls.Add(1) == 2 {
			return syncer.Report{}, nil, errs.SnapshotInconsistent("a file vanished")
		}
		return old(ctx, o)
	}
	t.Cleanup(func() { runSync = old })
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	w.touch()
	w.waitFor("the error line", func() bool { return len(kinds(w.out.lines(t), "error")) > 0 })
	e := kinds(w.out.lines(t), "error")[0]["error"].(map[string]any)
	if e["code"] != "snapshot_inconsistent" || e["message"] == "" || e["fix"] == "" {
		t.Fatalf("error line: %v", e)
	}
	// The failed attempt is retried on a later poll and succeeds.
	w.waitFor("the retry's sync line", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestWatchEnvironmentErrorExits3(t *testing.T) {
	fastWatch(t)
	e := newEnv(t)
	e.root = filepath.Join(t.TempDir(), "missing")
	code, stdout, stderr := e.run("watch", "--every", "30ms")
	if code != 3 {
		t.Fatalf("exit %d, want 3\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if got := errorOf(t, stderr)["code"]; got != "teams_not_installed" {
		t.Fatalf("code = %v", got)
	}
}

func TestWatchUsage(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"watch", "--every", "0s"}, {"watch", "--every", "-5s"}, {"watch", "--min-interval", "-1s"}, {"watch", "--fields", "nope"}} {
		code, _, stderr := e.run(args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, stderr)
		}
	}
}

// A burst of events is one sync, and no sync starts before the quiet period has passed.
func TestWatchDebouncesBursts(t *testing.T) {
	w := newWatchEnv(t)
	ch := make(chan struct{}, 64)
	old := watchEvents
	watchEvents = func(context.Context, []string) (<-chan struct{}, func(), error) { return ch, func() {}, nil }
	t.Cleanup(func() { watchEvents = old })
	var calls atomic.Int32
	oldSync := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls.Add(1)
		return oldSync(ctx, o)
	}
	t.Cleanup(func() { runSync = oldSync })
	watchQuiet = 150 * time.Millisecond
	w.start("watch", "--every", "1h", "--min-interval", "0")
	w.baselineDone()
	w.touch()
	before := calls.Load()
	// The channel is buffered, so the whole burst lands at once; stamp the clock before the last
	// event so a descheduled test goroutine cannot make the quiet period look shorter than it was.
	var last time.Time
	for i := 0; i < 20; i++ {
		last = time.Now()
		ch <- struct{}{}
	}
	w.waitFor("the burst's sync", func() bool { return calls.Load() > before })
	if d := time.Since(last); d < 100*time.Millisecond {
		t.Fatalf("sync started %v after the last event, want the quiet period first", d)
	}
	time.Sleep(400 * time.Millisecond)
	if n := calls.Load() - before; n != 1 {
		t.Fatalf("burst caused %d syncs, want 1", n)
	}
}

// The real file watcher (FSEvents/kqueue/inotify) sees a write and triggers a sync well before the
// poll interval would.
func TestWatchFileEvents(t *testing.T) {
	w := newWatchEnv(t)
	w.start("watch", "--every", "1h")
	w.baselineDone()
	w.forget(`rowid=(select min(rowid) from messages)`)
	w.touch()
	f := filepath.Join(w.sources()[0].LevelDBDir, "000999.log.new")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil { // a new file is an event too
		t.Fatal(err)
	}
	w.waitFor("a sync from a file event", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
}

// firstLog is an existing .log (or .ldb) file in the first source's leveldb directory.
func (w *watchEnv) firstLog() string {
	w.t.Helper()
	dir := w.sources()[0].LevelDBDir
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".log") && !strings.HasPrefix(e.Name(), "LOG") {
			return filepath.Join(dir, e.Name())
		}
	}
	w.t.Fatal("no .log file in the fixture's leveldb dir")
	return ""
}

// Teams appends to the live .log file: no create, no mtime-only change. The real watcher must see it.
func TestWatchFileEventsAppend(t *testing.T) {
	w := newWatchEnv(t)
	w.start("watch", "--every", "1h")
	w.baselineDone()
	time.Sleep(300 * time.Millisecond) // let the watcher settle after the baseline
	f, err := os.OpenFile(w.firstLog(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	w.waitFor("a sync from an append", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
}

// LevelDB renames a finished temp file into place (MANIFEST, CURRENT); the real watcher must see it.
func TestWatchFileEventsRename(t *testing.T) {
	w := newWatchEnv(t)
	w.start("watch", "--every", "1h")
	w.baselineDone()
	time.Sleep(300 * time.Millisecond)
	dir := w.sources()[0].LevelDBDir
	tmp := filepath.Join(dir, "000998.dbtmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Drain the create event's sync, then rename and expect another.
	w.waitFor("a sync from the create", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	n := len(kinds(w.out.lines(t), "sync"))
	if err := os.Rename(tmp, filepath.Join(dir, "000998.ldb.moved")); err != nil {
		t.Fatal(err)
	}
	w.waitFor("a sync from the rename", func() bool { return len(kinds(w.out.lines(t), "sync")) > n })
}

// Events that never stop must not postpone the sync forever: it starts at most watchMaxWait after
// the burst's first event.
func TestWatchDebounceMaxWait(t *testing.T) {
	w := newWatchEnv(t)
	ch := make(chan struct{}, 64)
	old := watchEvents
	watchEvents = func(context.Context, []string) (<-chan struct{}, func(), error) { return ch, func() {}, nil }
	t.Cleanup(func() { watchEvents = old })
	var calls atomic.Int32
	oldSync := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls.Add(1)
		return oldSync(ctx, o)
	}
	t.Cleanup(func() { runSync = oldSync })
	w.start("watch", "--every", "1h", "--min-interval", "0")
	w.baselineDone()
	watchQuiet, watchMaxWait = 200*time.Millisecond, 400*time.Millisecond
	w.touch()
	before := calls.Load()
	stop := make(chan struct{})
	go func() { // an event every 20 ms: the quiet period never elapses
		for {
			select {
			case <-stop:
				return
			case ch <- struct{}{}:
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	defer close(stop)
	// The stream never goes quiet, so a sync here can only come from the max wait; waitFor bounds
	// how long we wait for it without a wall-clock upper limit that a loaded machine could trip.
	w.waitFor("a sync during the event stream", func() bool { return calls.Load() > before })
}

// A lock held briefly by another run delays the watch by a short backoff, not by --every.
func TestWatchLockedRetriesQuickly(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	release, err := store.AcquireLock(w.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w.start("watch", "--every", "1h")
	w.waitFor("the locked warning", func() bool { return strings.Contains(w.errb.String(), "locked") })
	release()
	w.baselineDone() // would take an hour if the retry waited for --every
}

// If the changed rows cannot be read back after a successful sync, say so instead of dropping them.
func TestWatchReadbackFailureIsReported(t *testing.T) {
	skipIfRoot(t)
	w := newWatchEnv(t)
	noEvents(t)
	var calls atomic.Int32
	old := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		rep, ch, err := old(ctx, o)
		if calls.Add(1) == 2 {
			_ = os.Chmod(w.db, 0) // the archive can no longer be opened for the read-back
			t.Cleanup(func() { _ = os.Chmod(w.db, 0o600) })
		}
		return rep, ch, err
	}
	t.Cleanup(func() { runSync = old })
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	w.forget(`rowid=(select min(rowid) from messages)`)
	w.touch()
	w.waitFor("the error line", func() bool { return len(kinds(w.out.lines(t), "error")) > 0 })
	e := kinds(w.out.lines(t), "error")[0]["error"].(map[string]any)
	if !strings.Contains(e["message"].(string), "1 change") || e["fix"] == "" {
		t.Fatalf("error line: %v", e)
	}
}

// A baseline that failed and was followed by a successful sync is silent about its changes; say so.
func TestWatchLateBaselineWarns(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	var calls atomic.Int32
	old := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		if calls.Add(1) == 1 {
			return syncer.Report{}, nil, errs.SnapshotInconsistent("a file vanished")
		}
		return old(ctx, o)
	}
	t.Cleanup(func() { runSync = old })
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	w.waitFor("the late-baseline warning", func() bool { return strings.Contains(w.errb.String(), "baseline_delayed") })
}

// eventfulWatch makes watch take its events from the returned channel and counts the syncs.
func eventfulWatch(t *testing.T) (ch chan struct{}, calls *atomic.Int32) {
	t.Helper()
	ch = make(chan struct{}, 64)
	old := watchEvents
	watchEvents = func(context.Context, []string) (<-chan struct{}, func(), error) { return ch, func() {}, nil }
	t.Cleanup(func() { watchEvents = old })
	calls = new(atomic.Int32)
	oldSync := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls.Add(1)
		return oldSync(ctx, o)
	}
	t.Cleanup(func() { runSync = oldSync })
	return ch, calls
}

// The next sync starts no sooner than --min-interval after the previous one ended, however quiet
// the cache is, and events that arrive during the sync and the wait become exactly one sync.
func TestWatchMinIntervalCoalescesBusyCache(t *testing.T) {
	w := newWatchEnv(t)
	ch, calls := eventfulWatch(t)
	var ended atomic.Int64 // when the second sync's predecessor ended, unix nanos
	var gap atomic.Int64   // time from that end to the next sync's start
	inner := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		if prev := ended.Load(); prev != 0 && gap.Load() == 0 {
			gap.Store(time.Now().UnixNano() - prev)
		}
		if calls.Load() == 0 { // the baseline sync: a burst of events lands while it runs
			for i := 0; i < 5; i++ {
				ch <- struct{}{}
			}
		}
		rep, c, err := inner(ctx, o)
		if ended.Load() == 0 {
			ended.Store(time.Now().UnixNano())
		}
		return rep, c, err
	}
	t.Cleanup(func() { runSync = inner })
	const interval = 300 * time.Millisecond
	w.start("watch", "--every", "1h", "--min-interval", interval.String())
	w.baselineDone()
	w.touch()                 // the cache really changed, so the sync after the wait has work to do
	for i := 0; i < 20; i++ { // a busy cache keeps writing during the wait
		ch <- struct{}{}
	}
	w.waitFor("the sync after the wait", func() bool { return calls.Load() >= 2 })
	if g := time.Duration(gap.Load()); g < interval {
		t.Fatalf("second sync started %v after the first ended, want at least %v", g, interval)
	}
	time.Sleep(2 * interval)
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d syncs for a burst of events, want 2 (baseline, then one coalesced)", n)
	}
}

// Events during the wait with nothing actually changed run no sync at all.
func TestWatchMinIntervalNoChangeNoSync(t *testing.T) {
	w := newWatchEnv(t)
	ch, calls := eventfulWatch(t)
	w.start("watch", "--every", "1h", "--min-interval", "100ms")
	w.baselineDone()
	for i := 0; i < 5; i++ {
		ch <- struct{}{}
	}
	time.Sleep(400 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d syncs, want only the baseline", n)
	}
}

// A stop signal during the wait ends watch at once with exit 0.
func TestWatchStopDuringMinIntervalWait(t *testing.T) {
	w := newWatchEnv(t)
	ch, calls := eventfulWatch(t)
	w.start("watch", "--every", "1h", "--min-interval", "1h")
	w.baselineDone()
	w.touch()
	ch <- struct{}{}
	time.Sleep(100 * time.Millisecond) // now waiting out the hour
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d syncs, want only the baseline before the wait ends", n)
	}
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

// A held lock retries on its short backoff even under a long --min-interval: nothing was synced.
func TestWatchLockedRetryIgnoresMinInterval(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	release, err := store.AcquireLock(w.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	w.start("watch", "--every", "1h", "--min-interval", "1h")
	w.waitFor("the locked warning", func() bool { return strings.Contains(w.errb.String(), "locked") })
	release()
	w.baselineDone()
}

// The pause can be set from the environment, and the environment value is validated like the flag.
func TestWatchMinIntervalFromEnvironment(t *testing.T) {
	e := newEnv(t)
	t.Setenv("TEAMSCRAWL_WATCH_MIN_INTERVAL", "-1s")
	if code, _, stderr := e.run("watch"); code != 2 {
		t.Fatalf("exit %d, want 2 (%s)", code, stderr)
	}
}
