package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// stubSync replaces the sync with fn for the test and counts the calls.
func stubSync(t *testing.T, fn func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error)) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	old := runSync
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls.Add(1)
		return fn(ctx, o)
	}
	t.Cleanup(func() { runSync = old })
	return &calls
}

func partialReport() syncer.Report {
	return syncer.Report{
		Status: syncer.StatusPartial,
		Sources: []syncer.SourceReport{
			{Source: "p1|o", Status: syncer.StatusOK, Accounts: []string{"t/u"}, Counts: &syncer.SourceCounts{}},
			{Source: "p2|o", Status: syncer.StatusFailed, Error: &syncer.SourceError{Code: errs.CodeNoFullDiskAccess, Message: "denied"}},
		},
		Omissions: map[string]int{"blob_missing": 1}, OtherOrigins: []string{},
	}
}

func partialErr() error { return errs.PartialSync("p2|o (no_full_disk_access)") }

// --- version ---

func TestVersionIsOneJSONDocumentAndAFlag(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	version, commit, date = "1.2.3", "abc1234", "2026-10-01T00:00:00Z"
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	for _, args := range [][]string{{"version"}, {"--version"}, {"--json", "--version"}} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 0 || errb.Len() != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, errb.String())
		}
		m := decode(t, out.String())
		if len(m) != 3 || m["version"] != "1.2.3" || m["commit"] != "abc1234" || m["date"] != "2026-10-01T00:00:00Z" {
			t.Fatalf("%v: %v", args, m)
		}
	}
	for _, args := range [][]string{{"version", "--format", "text"}, {"--version", "--format", "text"}} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if got := strings.TrimSpace(out.String()); got != "teamscrawl 1.2.3 (commit abc1234, built 2026-10-01T00:00:00Z)" {
			t.Fatalf("%v: %q", args, got)
		}
	}
}

// --- partial sync ---

func TestSyncPartialPrintsTheReportAndAPartialSyncError(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return partialReport(), nil, partialErr()
	})
	e := newEnv(t)
	code, stdout, stderr := e.run("sync", "--json")
	if code != errs.ExitRuntime {
		t.Fatalf("exit %d", code)
	}
	m := decode(t, stdout)
	srcs, _ := m["sources"].([]any)
	if m["status"] != "partial" || len(srcs) != 2 {
		t.Fatalf("report: %v", m)
	}
	failed := srcs[1].(map[string]any)
	if failed["status"] != "failed" || failed["error"].(map[string]any)["code"] != errs.CodeNoFullDiskAccess {
		t.Fatalf("failed source: %v", failed)
	}
	body := errorOf(t, stderr)
	if body["code"] != errs.CodePartialSync || !strings.Contains(body["message"].(string), "p2|o (no_full_disk_access)") || !strings.Contains(body["fix"].(string), "teamscrawl doctor") {
		t.Fatalf("error: %v", body)
	}
	// Text mode prints the report, names the failed source and still ends with the error.
	code, stdout, stderr = e.run("sync", "--format", "text", "--no-color")
	if code != errs.ExitRuntime || !strings.Contains(stdout, "partial") || !strings.Contains(stdout, "failed p2|o: no_full_disk_access: denied") || !strings.Contains(stderr, "error: some Teams sources synced") {
		t.Fatalf("text: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestSyncTotalFailurePrintsNoReport(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{Status: syncer.StatusFailed}, nil, errs.StoreMissing("x")
	})
	e := newEnv(t)
	code, stdout, stderr := e.run("sync", "--json")
	if code != errs.ExitRuntime || stdout != "" || errorOf(t, stderr)["code"] != errs.CodeStoreMissing {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestImplicitSyncPartialIsASyncErrorBesideTheResult(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return partialReport(), nil, partialErr()
	})
	e := newEnv(t)
	code, stdout, stderr := e.run("people", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	se, _ := decode(t, stdout)["sync_error"].(map[string]any)
	if se["code"] != errs.CodePartialSync || !strings.Contains(se["message"].(string), "p2|o") {
		t.Fatalf("sync_error = %v", se)
	}
	if !strings.Contains(stderr, `"warning"`) || !strings.Contains(stderr, errs.CodePartialSync) {
		t.Fatalf("stderr = %s", stderr)
	}
}

// --- freshness per account ---

func TestFreshnessIsPerAccount(t *testing.T) {
	e := newEnv(t)
	e.sync()
	calls := stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errs.StoreMissing("stub")
	})
	acctKey := tenantA + "/" + userA
	// A sync filtered to one account refreshes only that account: pretend the last full sync is gone.
	e.exec(`update sync_runs set accounts_json='["` + acctKey + `"]' where accounts_json is not null`)
	for _, c := range []struct {
		args []string
		sync bool
	}{
		{[]string{"people", "--account", acctKey}, false},
		{[]string{"people"}, true}, // the other account is stale
		{[]string{"people", "--account", "00000000-0000-4000-8000-000000000002/00000000-0000-4000-8000-0000000000a2"}, true},
	} {
		before := calls.Load()
		code, stdout, stderr := e.run(append(c.args, "--json")...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", c.args, code, stderr)
		}
		if got := calls.Load() > before; got != c.sync {
			t.Errorf("%v: implicit sync attempted = %v, want %v", c.args, got, c.sync)
		}
		if _, has := decode(t, stdout)["sync_error"]; has != c.sync {
			t.Errorf("%v: sync_error present = %v", c.args, has)
		}
	}
}

func TestPartialSyncDoesNotRefreshAndReadsRetry(t *testing.T) {
	e := newEnv(t)
	// The first run is partial: the stub commits nothing, so the archive stays never-synced.
	calls := stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return partialReport(), nil, partialErr()
	})
	for i := 0; i < 2; i++ {
		code, stdout, _ := e.run("people", "--json")
		m := decode(t, stdout)
		if code != 0 || m["archive_age_seconds"] != nil || m["sync_error"] == nil {
			t.Fatalf("read %d: exit %d, %v", i, code, m)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("each read must retry the implicit sync, got %d attempts", calls.Load())
	}
}

// --- hints ---

func TestNoPlainHintLineInJSONMode(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errs.StoreMissing("stub")
	})
	e := newEnv(t)
	_, stdout, stderr := e.run("people", "--json")
	if strings.Contains(stderr, "hint:") {
		t.Fatalf("JSON mode stderr must be JSON lines only: %q", stderr)
	}
	if m := decode(t, stdout); m["needs_sync"] != true || m["hint"] == "" {
		t.Fatalf("the hint stays in the JSON: %v", m)
	}
	_, _, stderr = e.run("people", "--format", "text")
	if !strings.Contains(stderr, "hint: run teamscrawl sync") {
		t.Fatalf("text mode keeps the hint line: %q", stderr)
	}
}

// --- argument validation comes before the implicit sync ---

func TestTeamIsValidatedBeforeTheImplicitSync(t *testing.T) {
	e := newEnv(t)
	e.sync()
	calls := stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, nil
	})
	for _, args := range [][]string{{"messages"}, {"search", "x"}, {"unread"}, {"conversations"}, {"activity"}} {
		code, _, stderr := e.run(append(args, "--team", "no-such-team", "--max-age", "1ns", "--json")...)
		if code != errs.ExitUsage || errorOf(t, stderr)["code"] != errs.CodeUsage {
			t.Errorf("%v: exit %d, %s", args, code, stderr)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("a bad --team cost %d syncs", calls.Load())
	}
}

func TestTeamWithoutAnArchiveIsAUsageErrorNotAnEmptyList(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errs.StoreMissing("stub")
	})
	e := newEnv(t)
	code, stdout, stderr := e.run("messages", "--team", "nope", "--json")
	if code != errs.ExitUsage || stdout != "" || errorOf(t, stderr)["code"] != errs.CodeUsage {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestTeamPassesWhenItExists(t *testing.T) {
	e := newEnv(t)
	e.sync()
	// Any team that the fixture holds works; reading the list proves the pre-check accepts it.
	code, stdout, _ := e.run("conversations", "--kind", "Space", "--json")
	if code != 0 {
		t.Fatal("conversations")
	}
	for _, it := range items(t, decode(t, stdout)) {
		if name, _ := it["display_name"].(string); name != "" && it["team_id"] == it["id"] {
			code, _, stderr := e.run("conversations", "--team", it["id"].(string), "--json")
			if code != 0 {
				t.Fatalf("--team %v: %s", it["id"], stderr)
			}
			return
		}
	}
	t.Skip("the fixture has no team")
}

// --- whoami ---

func TestWhoamiNestedArchiveCarriesTheSameMeta(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("whoami", "--json")
	m := decode(t, stdout)
	nested := m["archive"].(map[string]any)
	if m["archive_age_seconds"] == nil || nested["archive_age_seconds"] != m["archive_age_seconds"] {
		t.Fatalf("top %v, nested %v", m["archive_age_seconds"], nested["archive_age_seconds"])
	}
	// Never synced: both say so.
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errs.StoreMissing("stub")
	})
	e2 := newEnv(t)
	_, stdout, _ = e2.run("whoami", "--json")
	m = decode(t, stdout)
	nested = m["archive"].(map[string]any)
	if m["needs_sync"] != true || nested["needs_sync"] != true || nested["hint"] != m["hint"] || nested["sync_error"] == nil || m["sync_error"] == nil {
		t.Fatalf("top %v nested %v", m, nested)
	}
}

// --- sql ---

func TestSQLAcceptsWordsInLiteralsAndExplainsEngineErrors(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "sql", "select 'attach' as w, 'detach; pragma' as v", "--json")
	if code != 0 {
		t.Fatalf("literal words: exit %d: %s", code, stderr)
	}
	if rows := decode(t, stdout)["rows"].([]any); len(rows) != 1 {
		t.Fatalf("rows: %v", rows)
	}
	code, _, stderr = e.run("--max-age", "0", "sql", "select * from no_such_table", "--json")
	body := errorOf(t, stderr)
	if code != errs.ExitUsage || strings.Contains(body["fix"].(string), "read-only by design") || !strings.Contains(body["fix"].(string), "sqlite_master") {
		t.Fatalf("engine error: %d %v", code, body)
	}
	// Statements that are not reads keep the read-only explanation, including attach.
	for _, q := range []string{"attach database ':memory:' as x", "select 1; select 2", "delete from messages"} {
		code, _, stderr = e.run("--max-age", "0", "sql", q, "--json")
		body = errorOf(t, stderr)
		if code != errs.ExitUsage || !strings.Contains(body["fix"].(string), "read-only by design") {
			t.Errorf("%q: %d %v", q, code, body)
		}
	}
	// The cut is made while streaming: no total, truncated says more exist.
	_, stdout, _ = e.run("--max-age", "0", "sql", "select id from messages", "--limit", "2", "--json")
	m := decode(t, stdout)
	if m["truncated"] != true || m["count"].(float64) != 2 {
		t.Fatalf("cut: %v", m)
	}
	if _, has := m["total"]; has {
		t.Fatal("sql no longer counts rows past the limit")
	}
}

// --- doctor ---

func TestDoctorReportsAnArchiveFromANewerBuild(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.exec(`update meta set value='99' where key='derivation_version'`)
	code, stdout, _ := e.run("doctor", "--json")
	c := checks(t, decode(t, stdout))["archive_newer"]
	if code != 3 || c["ok"] != false || !strings.Contains(c["detail"].(string), "99") || !strings.Contains(c["fix"].(string), "brew upgrade") {
		t.Fatalf("exit %d: %v", code, c)
	}
	e.exec(`update meta set value='1' where key='derivation_version'`)
	_, stdout, _ = e.run("doctor", "--json")
	if c := checks(t, decode(t, stdout))["archive_newer"]; c["ok"] != true {
		t.Fatalf("an older archive is fine: %v", c)
	}
	e.exec(`delete from meta where key='derivation_version'`)
	_, stdout, _ = e.run("doctor", "--json")
	if c := checks(t, decode(t, stdout))["archive_newer"]; c["ok"] != true {
		t.Fatalf("no derivation row is alpha.1, which is older: %v", c)
	}
}

func TestDoctorFlagsAPartialOrFailedLastSync(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, _ := e.run("doctor", "--json")
	if c := checks(t, decode(t, stdout))["last_sync_status"]; code != 0 || c["ok"] != true || c["warn"] == true {
		t.Fatalf("clean: %d %v", code, c)
	}
	for _, status := range []string{"partial", "failed"} {
		e.exec(`insert into sync_runs(started_at, finished_at, source, status, accounts_json) values('2026-01-01T00:00:00.000Z','2026-01-01T00:00:01.000Z','','` + status + `','["*"]')`)
		code, stdout, _ = e.run("doctor", "--json")
		c := checks(t, decode(t, stdout))["last_sync_status"]
		if code != 0 || c["ok"] != true || c["warn"] != true || !strings.Contains(c["detail"].(string), status) || !strings.Contains(c["fix"].(string), "teamscrawl sync") {
			t.Fatalf("%s: exit %d, %v", status, code, c)
		}
	}
	// Before any sync there is no last run to judge.
	e2 := newEnv(t)
	_, stdout, _ = e2.run("doctor", "--json")
	if _, has := checks(t, decode(t, stdout))["last_sync_status"]; has {
		t.Fatal("no archive, no last-sync status check")
	}
}

// --- signals ---

func TestSecondSignalForcesAnExit(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	cancelled := make(chan struct{})
	exited := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		watchSignals(sigs, func() { close(cancelled) }, func(code int) { exited <- code })
		close(done)
	}()
	sigs <- syscall.SIGINT
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the first signal must start a graceful stop")
	}
	select {
	case code := <-exited:
		t.Fatalf("one signal must not force an exit (code %d)", code)
	case <-time.After(50 * time.Millisecond):
	}
	sigs <- syscall.SIGTERM
	select {
	case code := <-exited:
		if code != exitForced {
			t.Fatalf("forced exit code %d, want %d", code, exitForced)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal must force an exit")
	}
	<-done
	// Closing the channel (the process is done) ends the watcher without a forced exit.
	closed := make(chan os.Signal)
	close(closed)
	watchSignals(closed, func() { t.Error("cancel on a closed channel") }, func(int) { t.Error("exit on a closed channel") })
	if exitForced != 130 {
		t.Fatalf("exitForced = %d", exitForced)
	}
}

func TestMainStopsSignalHandlingWhenItReturns(t *testing.T) {
	var out, errb bytes.Buffer
	for i := 0; i < 3; i++ { // each Main installs and removes its own handler
		if code := Main([]string{"version"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
}

// --- watch ---

func TestWatchHTMLFieldIsFilled(t *testing.T) {
	w := newWatchEnv(t)
	noEvents(t)
	w.start("watch", "--every", "30ms", "--emit-initial", "--fields", "id,html")
	w.waitFor("message lines", func() bool { return len(kinds(w.out.lines(t), "message")) > 0 })
	var withHTML int
	for _, l := range kinds(w.out.lines(t), "message") {
		item := l["item"].(map[string]any)
		if h, _ := item["html"].(string); h != "" {
			withHTML++
		}
		if len(item) > 2 {
			t.Fatalf("item has more than the asked fields: %v", item)
		}
	}
	if withHTML == 0 {
		t.Fatal("--fields html must fill html on messages that have it")
	}
}

// secondSource copies the first source into a second origin, so a sync has two sources; breaking
// one of them makes the sync partial.
func (w *watchEnv) secondSource() (healthy, broken teamsdesktop.Source) {
	w.t.Helper()
	first := w.sources()[0]
	idb := filepath.Dir(first.LevelDBDir)
	copyTree(w.t, first.LevelDBDir, filepath.Join(idb, "https_teams.cloud.microsoft_0.indexeddb.leveldb"))
	copyTree(w.t, first.BlobDir, filepath.Join(idb, "https_teams.cloud.microsoft_0.indexeddb.blob"))
	srcs := w.sources()
	if len(srcs) != 2 {
		w.t.Fatalf("sources: %d", len(srcs))
	}
	return srcs[0], srcs[1]
}

func TestWatchPartialSyncEmitsTheCommittedChangesThenAnError(t *testing.T) {
	skipIfRoot(t)
	w := newWatchEnv(t)
	noEvents(t)
	healthy, broken := w.secondSource()
	w.start("watch", "--every", "30ms")
	w.baselineDone()
	time.Sleep(200 * time.Millisecond)
	// Break the second source, then change the first: the second fails, the first commits.
	manifest := filepath.Join(broken.LevelDBDir, "MANIFEST-000001")
	if err := os.Chmod(manifest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o600) })
	if err := os.WriteFile(filepath.Join(broken.LevelDBDir, "touch"), []byte("x"), 0o600); err != nil { // the broken source changed too
		t.Fatal(err)
	}
	w.forget(`rowid=(select min(rowid) from messages)`)
	ts := time.Now().Add(3 * time.Hour)
	_ = os.Chtimes(filepath.Join(healthy.LevelDBDir, "000003.log"), ts, ts)
	// The error line is deduplicated and can come from a tick before the change is made, so wait
	// for both; the order within one tick (changes, then the error) is pinned by the unit tests.
	w.waitFor("the error line and the change", func() bool {
		return len(kinds(w.out.lines(t), "error")) > 0 && len(kinds(w.out.lines(t), "message")) > 0
	})
	ls := w.out.lines(t)
	var order []string
	for _, l := range ls {
		order = append(order, l["kind"].(string))
	}
	news := 0
	for _, l := range ls {
		if l["kind"] == "message" && l["change"] == "new" {
			news++
		}
	}
	if news != 1 {
		t.Fatalf("the committed source's change must not be lost: %v", order)
	}
	e := kinds(ls, "error")[0]["error"].(map[string]any)
	if e["code"] != errs.CodePartialSync || !strings.Contains(e["message"].(string), "https_teams.microsoft.com_0") {
		t.Fatalf("error line: %v", e)
	}
	// Repair the second source: the retry syncs it normally and nothing is emitted twice.
	_ = os.Chmod(manifest, 0o600)
	w.waitFor("a clean sync after the repair", func() bool {
		for _, l := range kinds(w.out.lines(t), "sync") {
			if r, _ := l["report"].(map[string]any); r["status"] == "ok" {
				return true
			}
		}
		return false
	})
	if n := w.archiveCount("select count(*) from sync_runs where status='partial'"); n < 1 {
		t.Fatalf("partial runs recorded: %d", n)
	}
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errb.String())
	}
}

func TestWatcherPartialSyncRules(t *testing.T) {
	e := newEnv(t)
	e.sync() // the archive exists, so the stubbed change keys can be looked up (and are not found)
	changes := []syncer.Change{{Kind: "message", Change: "new", Key: "t|u|c|m"}}
	run := func(emitInitial, baselined bool) (*watcher, *syncBuf, *syncBuf, error) {
		w, out, errb := jsonWatcher(t, e.root, e.db)
		w.emitInitial, w.baselined = emitInitial, baselined
		stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
			return partialReport(), changes, partialErr()
		})
		done, err := w.sync()
		if done {
			t.Fatal("a partial sync is not done: the failed source must be retried")
		}
		return w, out, errb, err
	}
	// Baselined: the committed changes are emitted (here the keys do not resolve, so only the sync
	// line appears), the fingerprints stay stale so the next pass retries, and the error is returned.
	w, out, _, err := run(false, true)
	codedErr(t, err, errs.CodePartialSync)
	if len(kinds(out.lines(t), "sync")) != 1 || w.fps != nil {
		t.Fatalf("lines %v, fps %v", out.lines(t), w.fps)
	}
	// The first sync that commits anything is the baseline: nothing is emitted, and the baseline
	// is taken, so later partial ticks emit.
	w, out, _, err = run(false, false)
	codedErr(t, err, errs.CodePartialSync)
	if out.String() != "" || !w.baselined || w.fps != nil {
		t.Fatalf("out %q, baselined %v", out.String(), w.baselined)
	}
	// With --emit-initial the committed changes are emitted for that first sync too.
	w, out, _, err = run(true, false)
	codedErr(t, err, errs.CodePartialSync)
	if len(kinds(out.lines(t), "sync")) != 1 || !w.baselined {
		t.Fatalf("out %q, baselined %v", out.String(), w.baselined)
	}
	// A read-back failure while emitting is reported and does not hide the partial error.
	w, out, _ = func() (*watcher, *syncBuf, *syncBuf) {
		w, o, eb := jsonWatcher(t, e.root, filepath.Join(t.TempDir(), "absent.db"))
		return w, o, eb
	}()
	w.baselined = true
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return partialReport(), changes, partialErr()
	})
	_, err = w.sync()
	codedErr(t, err, errs.CodePartialSync)
	if es := kinds(out.lines(t), "error"); len(es) != 1 || !strings.Contains(es[0]["error"].(map[string]any)["message"].(string), "1 change") {
		t.Fatalf("lines %v", out.lines(t))
	}
}

func codedErr(t *testing.T, err error, code string) {
	t.Helper()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != code {
		t.Fatalf("want coded %s, got %v", code, err)
	}
}

var _ = store.ErrNoArchive

func TestWritingTheVersionCanFail(t *testing.T) {
	var errb bytes.Buffer
	if code := runCLI(context.Background(), []string{"--version", "--json"}, failingWriter{}, &errb); code != errs.ExitRuntime || !strings.Contains(errb.String(), "pipe closed") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSyncPartialReportWriteFailure(t *testing.T) {
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return partialReport(), nil, partialErr()
	})
	e := newEnv(t)
	var errb bytes.Buffer
	code := runCLI(context.Background(), []string{"--db", e.db, "--teams-root", e.root, "--json", "sync"}, failingWriter{}, &errb)
	if code != errs.ExitRuntime || !strings.Contains(errb.String(), "pipe closed") || strings.Contains(errb.String(), errs.CodePartialSync) {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestSignalWatcherEndsWhenTheChannelClosesAfterTheFirstSignal(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	cancelled := 0
	sigs <- syscall.SIGINT
	close(sigs)
	watchSignals(sigs, func() { cancelled++ }, func(int) { t.Error("forced exit without a second signal") })
	if cancelled != 1 {
		t.Fatalf("cancelled %d times", cancelled)
	}
}

func TestDoctorReportsAnUnreadableDerivationVersion(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.exec(`update meta set value='abc' where key='derivation_version'`)
	code, stdout, _ := e.run("doctor", "--json")
	c := checks(t, decode(t, stdout))["archive_newer"]
	if code != 3 || c["ok"] != false || !strings.Contains(c["detail"].(string), "not a number") {
		t.Fatalf("exit %d: %v", code, c)
	}
}

func TestTeamCheckReportsAnUnreadableArchive(t *testing.T) {
	e := newEnv(t)
	e.garbageArchive()
	code, _, stderr := e.run("--max-age", "0", "messages", "--team", "x", "--json")
	if code != errs.ExitRuntime || errorOf(t, stderr)["code"] != errs.CodeDBError {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestLostChangesNamesTheCause(t *testing.T) {
	if err := lostChanges(errors.New("odd"), 3); !strings.Contains(err.Error(), "3 change(s)") || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("%v", err)
	}
	var c *errs.Coded
	if err := lostChanges(errs.DBError(errors.New("x")), 2); !errors.As(err, &c) || c.Code != errs.CodeDBError || !strings.Contains(c.Message, "2 change(s)") {
		t.Fatalf("%v", err)
	}
}

func TestWatchItemsReportAFailedHTMLRead(t *testing.T) {
	e := newEnv(t)
	e.sync()
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, _, err := st.SQL(context.Background(), `select tenant_id||'|'||user_id||'|'||conversation_id||'|'||id from messages limit 1`, 1)
	_ = st.Close()
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	e.exec(`alter table messages drop column content_html`)
	w, _, _ := jsonWatcher(t, e.root, e.db)
	w.rt.fields = []string{"id", "html"}
	_, err = w.items([]syncer.Change{{Kind: "message", Change: "new", Key: rows[0][0].(string)}})
	codedErr(t, err, errs.CodeDBError)
}

func TestWatcherEmitsCommittedChangesWhenTheRunCannotBeRecorded(t *testing.T) {
	e := newEnv(t)
	e.sync()
	w, out, _ := jsonWatcher(t, e.root, e.db)
	w.baselined = true
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{Status: syncer.StatusOK}, []syncer.Change{{Kind: "message", Change: "new", Key: "t|u|c|m"}}, errs.DBError(errors.New("disk full"))
	})
	done, err := w.sync()
	codedErr(t, err, errs.CodeDBError)
	if done || len(kinds(out.lines(t), "sync")) != 1 || w.fps != nil {
		t.Fatalf("done %v, lines %v", done, out.lines(t))
	}
	// A failed run (nothing committed) is still just an error.
	stubSync(t, func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{Status: syncer.StatusFailed}, nil, errs.DBError(errors.New("x"))
	})
	out.b.Reset()
	if _, err := w.sync(); err == nil || out.String() != "" {
		t.Fatalf("err %v out %q", err, out.String())
	}
}

// Watch started while one source is already failing: the first partial sync is the baseline, and
// a change in the healthy source afterwards is emitted.
func TestWatchStartedWithABrokenSourceStillEmitsLaterChanges(t *testing.T) {
	skipIfRoot(t)
	w := newWatchEnv(t)
	noEvents(t)
	healthy, broken := w.secondSource()
	manifest := filepath.Join(broken.LevelDBDir, "MANIFEST-000001")
	if err := os.Chmod(manifest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o600) })
	w.start("watch", "--every", "30ms")
	w.waitFor("the baseline (partial) sync", func() bool { return w.archiveCount("select count(*) from sync_runs where status='partial'") > 0 })
	w.waitFor("the partial error line", func() bool { return len(kinds(w.out.lines(t), "error")) > 0 })
	if n := len(kinds(w.out.lines(t), "message")); n != 0 {
		t.Fatalf("the baseline must be silent, got %d message lines", n)
	}
	w.forget(`rowid=(select min(rowid) from messages)`)
	ts := time.Now().Add(5 * time.Hour)
	_ = os.Chtimes(filepath.Join(healthy.LevelDBDir, "000003.log"), ts, ts)
	w.waitFor("the healthy source's change", func() bool { return len(kinds(w.out.lines(t), "message")) == 1 })
	if code := w.stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errb.String())
	}
}

func TestDoctorWarnsAboutAnArchiveFromAnOlderVersion(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.exec(`alter table sync_runs drop column accounts_json`)
	code, stdout, stderr := e.run("doctor", "--json")
	c := checks(t, decode(t, stdout))["archive_upgrade"]
	if code != 0 || c["ok"] != true || c["warn"] != true || !strings.Contains(c["detail"].(string), "next sync upgrades it") {
		t.Fatalf("exit %d: %v %s", code, c, stderr)
	}
}

// sync --full-read (and TEAMSCRAWL_FULL_READ) asks the syncer to read every record in full.
func TestSyncFullReadFlag(t *testing.T) {
	var got []bool
	stubSync(t, func(_ context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		got = append(got, o.FullRead)
		return syncer.Report{Status: syncer.StatusOK}, nil, nil
	})
	e := newEnv(t)
	for _, args := range [][]string{{"sync", "--json"}, {"sync", "--json", "--full-read"}} {
		if code, _, stderr := e.run(args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, stderr)
		}
	}
	t.Setenv("TEAMSCRAWL_FULL_READ", "1")
	if code, _, stderr := e.run("sync", "--json"); code != 0 {
		t.Fatalf("env: exit %d\n%s", code, stderr)
	}
	if len(got) != 3 || got[0] || !got[1] || !got[2] {
		t.Fatalf("FullRead passed as %v", got)
	}
}
