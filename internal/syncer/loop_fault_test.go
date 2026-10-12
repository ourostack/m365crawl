package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/loopcache"
	"github.com/ourostack/m365crawl/internal/store"
)

var errLoopInjected = errors.New("injected loop archive fault")

// faultyLoop wraps the archive and fails the named method's call number at (1-based).
type faultyLoop struct {
	*store.Store
	method string
	at     int
	calls  map[string]int
	after  map[string]func()
}

func (f *faultyLoop) fault(method string) error {
	f.calls[method]++
	if method == f.method && f.calls[method] == f.at {
		return errLoopInjected
	}
	return nil
}

func (f *faultyLoop) LoopSources(ctx context.Context) ([]store.LoopSource, error) {
	if err := f.fault("sources"); err != nil {
		return nil, err
	}
	sources, err := f.Store.LoopSources(ctx)
	if after := f.after["sources"]; after != nil {
		after()
	}
	return sources, err
}

func (f *faultyLoop) LoopRawFrame(ctx context.Context, id int64) (store.LoopAcquisition, error) {
	if err := f.fault("frame"); err != nil {
		return store.LoopAcquisition{}, err
	}
	return f.Store.LoopRawFrame(ctx, id)
}

func (f *faultyLoop) LoopDiscoveryFrames(ctx context.Context) (store.LoopDiscovery, error) {
	if err := f.fault("discover"); err != nil {
		return store.LoopDiscovery{}, err
	}
	discovery, err := f.Store.LoopDiscoveryFrames(ctx)
	if after := f.after["discover"]; after != nil {
		after()
	}
	return discovery, err
}

func (f *faultyLoop) BeginLoopAttempt(ctx context.Context, registration store.LoopRegistration, at time.Time) (store.KnowledgeAttempt, error) {
	if err := f.fault("begin"); err != nil {
		return store.KnowledgeAttempt{}, err
	}
	return f.Store.BeginLoopAttempt(ctx, registration, at)
}

func (f *faultyLoop) CommitLoopObservations(ctx context.Context, attempt store.KnowledgeAttempt, frame store.LoopRawFrame, result loopcache.Result, at time.Time) error {
	if err := f.fault("commit"); err != nil {
		return err
	}
	return f.Store.CommitLoopObservations(ctx, attempt, frame, result, at)
}

func (f *faultyLoop) RecordLoopFailure(ctx context.Context, attempt store.KnowledgeAttempt, reason string, at time.Time) error {
	if err := f.fault("failure"); err != nil {
		return err
	}
	return f.Store.RecordLoopFailure(ctx, attempt, reason, at)
}

func openLoop(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func loopSnapshotRows(t *testing.T, path, query string) []store.LoopChannelRow {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	snapshot, err := st.LoopSnapshot(context.Background(), store.LoopFilter{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Rows
}

func faultArchive(t *testing.T) (*loopArchive, int64) {
	t.Helper()
	a := newLoopArchive(t)
	healthy := a.insert(t, "https://synthetic.invalid", "healthy", syntheticLoop(t, 1, 1, "kept words"))
	a.insert(t, "https://synthetic.invalid", "missing", nil)
	explicitLoop(t, a.path, 0)
	a.insert(t, "https://synthetic.invalid", "new", syntheticLoop(t, 1, 1, "new words"))
	a.set(t, healthy, syntheticLoop(t, 2, 2, "changed words"))
	return a, healthy
}

func TestLoopCancellationAndArchiveFaults(t *testing.T) {
	ctx := context.Background()
	for _, method := range []string{"sources", "frame", "discover", "begin", "commit", "failure"} {
		for _, at := range []int{1, 2, 3} {
			a, _ := faultArchive(t)
			st := openLoop(t, a.path)
			faulty := &faultyLoop{Store: st, method: method, at: at, calls: map[string]int{}}
			report, err := syncLoop(ctx, faulty, LoopOptions{DBPath: a.path, Explicit: true, Discover: true})
			if faulty.calls[method] < at {
				continue
			}
			if !errors.Is(err, errLoopInjected) || report.Projected != 0 || report.Sources != nil {
				t.Fatalf("%s #%d: archive fault became a successful report: %+v %v", method, at, report, err)
			}
			if rows := loopSnapshotRows(t, a.path, "kept"); len(rows) != 1 {
				t.Fatalf("%s #%d: archive fault erased last-good observations", method, at)
			}
		}
	}
	// Cancellation between sources, and between discovered candidates, stops the run.
	for _, method := range []string{"sources", "discover"} {
		a, _ := faultArchive(t)
		cancelled, cancel := context.WithCancel(ctx)
		faulty := &faultyLoop{Store: openLoop(t, a.path), calls: map[string]int{}, after: map[string]func(){method: cancel}}
		if _, err := syncLoop(cancelled, faulty, LoopOptions{DBPath: a.path, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled after %s: %v", method, err)
		}
		cancel()
	}
	a, _ := faultArchive(t)
	if rows := loopSnapshotRows(t, a.path, "kept"); len(rows) != 1 {
		t.Fatal("archive faults erased last-good observations")
	}
	// Cancellation during parsing is returned, recorded as cancelled, and keeps the last good rows.
	cancelled, cancel := context.WithCancel(ctx)
	read := loopRead
	loopRead = func(ctx context.Context, value []byte) (loopcache.Result, error) {
		cancel()
		return read(ctx, value)
	}
	t.Cleanup(func() { loopRead = read })
	report, err := SyncLoop(cancelled, LoopOptions{DBPath: a.path, Explicit: true, Discover: true})
	if !errors.Is(err, context.Canceled) || report.Sources != nil || report.Projected != 0 {
		t.Fatalf("cancellation became success: %+v %v", report, err)
	}
	loopRead = read
	if n := a.count(t, `select count(*) from knowledge_attempts where outcome='cancelled' and reason='loop_cache_cancelled'`); n != 1 {
		t.Fatalf("cancelled attempts %d", n)
	}
	if rows := loopSnapshotRows(t, a.path, "kept"); len(rows) != 1 {
		t.Fatal("cancellation erased last-good observations")
	}
	cancelled, cancel = context.WithCancel(ctx)
	cancel()
	if _, err := SyncLoop(cancelled, LoopOptions{DBPath: a.path, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before start: %v", err)
	}
}

func TestLoopSourceFailuresKeepPeersAndLastGood(t *testing.T) {
	a := newLoopArchive(t)
	record := a.insert(t, "https://synthetic.invalid", "source", syntheticLoop(t, 1, 1, "lastgood words"))
	explicitLoop(t, a.path, 0)
	read := loopRead
	t.Cleanup(func() { loopRead = read })
	for name, fault := range map[string]func(context.Context, []byte) (loopcache.Result, error){
		"panic": func(context.Context, []byte) (loopcache.Result, error) { panic("synthetic") },
		"generic": func(context.Context, []byte) (loopcache.Result, error) {
			return loopcache.Result{}, errors.New("synthetic")
		},
	} {
		a.set(t, record, syntheticLoop(t, 2, 2, name+" words"))
		loopRead = fault
		report := explicitLoop(t, a.path, 0)
		if report.Failed != 1 || report.Sources[0].Health.Reason != "loop_cache_reader_failed" || report.Sources[0].SnapshotMatch != "differs" {
			t.Fatalf("%s reader failure: %+v", name, report)
		}
	}
	// Input changed after acquisition: a fixed failed attempt, no implicit retry.
	loopRead = func(ctx context.Context, value []byte) (loopcache.Result, error) {
		a.set(t, record, syntheticLoop(t, 4, 4, "peer words"))
		return read(ctx, value)
	}
	report := explicitLoop(t, a.path, 0)
	if report.Failed != 1 || report.Projected != 0 || report.Sources[0].Health.Reason != "loop_cache_input_changed" {
		t.Fatalf("changed input: %+v", report)
	}
	if rows := loopSnapshotRows(t, a.path, "lastgood"); len(rows) != 1 || rows[0].Retained {
		t.Fatalf("changed input replaced last good: %+v", rows)
	}
	// A newer attempt supersedes the running one: publication and failure recording report it.
	peer := openLoop(t, a.path)
	supersede := func() {
		acquisition, err := peer.LoopRawFrame(context.Background(), report.Sources[0].Registration.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.BeginLoopAttempt(context.Background(), *acquisition.Registration, loopNow().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	loopRead = func(ctx context.Context, value []byte) (loopcache.Result, error) {
		supersede()
		return read(ctx, value)
	}
	report = explicitLoop(t, a.path, 0)
	if report.Projected != 0 || len(report.Diagnostics) != 1 || !strings.HasPrefix(report.Diagnostics[0], "stale_attempt:") {
		t.Fatalf("superseded publication: %+v", report)
	}
	loopRead = func(ctx context.Context, value []byte) (loopcache.Result, error) {
		supersede()
		return loopcache.Result{}, &loopcache.ReadError{Code: "loop_cache_snapshot_unsupported"}
	}
	report = explicitLoop(t, a.path, 0)
	if report.Failed != 0 || len(report.Diagnostics) != 1 || !strings.HasPrefix(report.Diagnostics[0], "stale_attempt:") {
		t.Fatalf("superseded failure: %+v", report)
	}
}

func TestLoopPartialAndMetadataOnly(t *testing.T) {
	a := newLoopArchive(t)
	partial := strings.Replace(syntheticLoop(t, 5, 7, "partial alpha", "partial bravo"),
		`"blobs":{},"trees":{"channels"`, `"blobs":{"orphan":"absent"},"trees":{"channels"`, 1)
	a.insert(t, "https://synthetic.invalid", "partial", partial)
	a.insert(t, "https://synthetic.invalid", "metadata", syntheticLoop(t, 3, 3))
	report := explicitLoop(t, a.path, 0)
	if report.Projected != 2 {
		t.Fatalf("partial/metadata-only projections: %+v", report)
	}
	source := sourceByKey(t, report.Sources, "partial")
	if source.Health.Status != "partial" || source.Health.Coverage != "partial" || source.Health.Losses["loop_cache_blob_missing"] != 1 {
		t.Fatalf("partial source: %+v", source.Health)
	}
	rows := loopSnapshotRows(t, a.path, "partial")
	if len(rows) != 2 || !rows[0].Snapshot.Partial || rows[0].Snapshot.MissingBlobs != 1 || rows[0].Snapshot.LatestSequence != 7 ||
		len(rows[0].Snapshot.Losses) != 1 || rows[0].Snapshot.Losses[0].Code != "loop_cache_blob_missing" {
		t.Fatalf("partial state not retained: %+v", rows)
	}
	if metadata := sourceByKey(t, report.Sources, "metadata"); metadata.Health.Coverage != "metadata_only" ||
		metadata.Health.Reason != "loop_cache_metadata_only" || metadata.LastSnapshotID <= 0 {
		t.Fatalf("metadata-only source: %+v", metadata)
	}
	again := explicitLoop(t, a.path, 0)
	if again.Unchanged != 2 || again.Projected != 0 {
		t.Fatalf("identical bytes re-projected: %+v", again)
	}
}

func TestLoopQualificationBackfill(t *testing.T) {
	a := newLoopArchive(t)
	a.insert(t, "https://synthetic.invalid", "qualified", syntheticLoop(t, 1, 1, "unchanged bytes"))
	explicitLoop(t, a.path, 0)
	previous := store.LoopQualification
	store.LoopQualification = "loop-cache-v2/mapper-v1"
	t.Cleanup(func() { store.LoopQualification = previous })
	now := loopNow
	loopNow = func() time.Time { return now().Add(48 * time.Hour) }
	t.Cleanup(func() { loopNow = now })
	report, err := SyncLoop(context.Background(), LoopOptions{DBPath: a.path, MaxAge: time.Hour})
	if err != nil || report.Projected != 1 || report.Unchanged != 0 || report.Sources[0].SnapshotQualification != store.LoopQualification ||
		report.Sources[0].SnapshotMatch != "matches" {
		t.Fatalf("parser-version refresh: %+v %v", report, err)
	}
	if rows := loopSnapshotRows(t, a.path, "unchanged"); len(rows) != 2 {
		t.Fatalf("prior qualification rows: %d", len(rows))
	}
}

func TestLoopDiscoveryLimitAndOpenFailures(t *testing.T) {
	ctx := context.Background()
	a := newLoopArchive(t)
	for i := range store.LoopDiscoveryBatch + 1 {
		a.insert(t, "https://synthetic.invalid", "k"+strings.Repeat("x", i), nil)
	}
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, Explicit: true, Discover: true}); !errors.Is(err, store.ErrLoopDiscoveryLimit) {
		t.Fatalf("discovery limit: %v", err)
	}
	if n := a.count(t, `select count(*) from knowledge_locators`); n != 0 {
		t.Fatal("overflow registered sources")
	}
	acquire, open, readOnly := loopAcquire, loopOpen, loopReadOnly
	t.Cleanup(func() { loopAcquire, loopOpen, loopReadOnly = acquire, open, readOnly })
	loopAcquire = func(string) (func(), error) { return nil, errLoopInjected }
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, Explicit: true}); !errors.Is(err, errLoopInjected) {
		t.Fatalf("lock failure: %v", err)
	}
	loopAcquire = acquire
	loopOpen = func(context.Context, string) (*store.Store, error) { return nil, errLoopInjected }
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, Explicit: true}); !errors.Is(err, errLoopInjected) {
		t.Fatalf("open failure: %v", err)
	}
	loopReadOnly = func(context.Context, string) (*store.Store, error) { return nil, errLoopInjected }
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, MaxAge: time.Hour}); !errors.Is(err, errLoopInjected) {
		t.Fatalf("read-only open failure: %v", err)
	}
	loopReadOnly = readOnly
	closed, err := store.OpenReadOnly(ctx, a.path)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	loopReadOnly = func(context.Context, string) (*store.Store, error) { return closed, nil }
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, MaxAge: time.Hour}); err == nil {
		t.Fatal("closed read-only archive read succeeded")
	}
}

func TestLoopOrdinaryReadRefreshesOnlyDueSources(t *testing.T) {
	a := newLoopArchive(t)
	recent := a.insert(t, "https://synthetic.invalid", "recent", syntheticLoop(t, 1, 1, "recent words"))
	due := a.insert(t, "https://synthetic.invalid", "due", syntheticLoop(t, 1, 1, "due words"))
	explicitLoop(t, a.path, 0)
	if _, err := a.db.Exec(`update knowledge_attempts set started_at='2000-01-01T00:00:00.000000000Z' where locator_id=(select locator_id from loop_locator_metadata where key_json='due')`); err != nil {
		t.Fatal(err)
	}
	a.set(t, recent, syntheticLoop(t, 2, 2, "recent changed"))
	a.set(t, due, syntheticLoop(t, 2, 2, "due changed"))
	report, err := SyncLoop(context.Background(), LoopOptions{DBPath: a.path, MaxAge: time.Hour})
	if err != nil || report.Skipped != 1 || report.Projected != 1 || len(report.Sources) != 2 ||
		sourceByKey(t, report.Sources, "recent").SnapshotMatch != "differs" || sourceByKey(t, report.Sources, "due").SnapshotMatch != "matches" {
		t.Fatalf("ordinary read refreshed the wrong sources: %+v %v", report, err)
	}
}
