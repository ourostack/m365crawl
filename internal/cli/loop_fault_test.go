package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/loopcache"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// stubLoopSync replaces the refresh for one test; calls are numbered from 1.
func stubLoopSync(t *testing.T, fn func(call int, opts syncer.LoopOptions) (syncer.LoopReport, error)) {
	t.Helper()
	old, calls := runLoopSync, 0
	t.Cleanup(func() { runLoopSync = old })
	runLoopSync = func(_ context.Context, opts syncer.LoopOptions) (syncer.LoopReport, error) {
		calls++
		return fn(calls, opts)
	}
}

func seedLoopFault(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	db := loopRecords(t, e)
	insertLoopCLI(t, db, "page", loopCLIValue(t, 3, [2]string{"native", "synthetic channel words"}))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	runOK(t, e, "loop", "sync")
	return e, items(t, decode(t, runOK(t, e, "--max-age", "0", "loop", "list")))[0]["id"].(string)
}

// breakLoopChannels makes every channel read fail without removing the table.
func breakLoopChannels(t *testing.T, e *env) {
	t.Helper()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`alter table loop_channels rename column markers to renamed`); err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, e *env, exit int, code string, args ...string) {
	t.Helper()
	got, _, stderr := e.run(args...)
	if got != exit || !strings.Contains(stderr, `"code":"`+code+`"`) {
		t.Fatalf("%v: exit %d, want %d %s: %s", args, got, exit, code, stderr)
	}
}

func TestLoopCommandErrors(t *testing.T) {
	e, id := seedLoopFault(t)
	for _, args := range [][]string{
		{"loop", "sync", "--source-id=-1"}, {"loop", "list", "--source-id=-1"}, {"loop", "sources", "--source-id=-1"},
		{"loop", "list", "--limit=-1"}, {"--fields", "bogus", "loop", "list"}, {"--fields", "bogus", "loop", "show", id},
		{"--fields", "bogus", "loop", "sources"}, {"--account", "t/u", "loop", "sync"}, {"--account", "t/u", "loop", "list"}, {"--account", "t/u", "loop", "show", id},
		{"--account", "t/u", "loop", "sources"}, {"loop", "show", "loop-channel:0"}, {"loop", "show", "loop-channel:01"},
		{"loop", "show", "channel:1"}, {"loop", "show", "loop-channel:x"},
	} {
		wantCode(t, e, 2, "usage", append([]string{"--max-age", "0"}, args...)...)
	}
	wantCode(t, e, 2, "loop_source_unknown", "--max-age", "0", "loop", "sources", "--source-id", "999")
	wantCode(t, e, 2, "loop_source_unknown", "loop", "sync", "--source-id", "999")
	wantCode(t, e, 2, "loop_source_unknown", "--max-age", "0", "loop", "list", "--source-id", "999")
	wantCode(t, e, 2, "loop_channel_unknown", "--max-age", "0", "loop", "show", "loop-channel:999")
	wantCode(t, newEnv(t), 2, "loop_channel_unknown", "--max-age", "0", "loop", "show", "loop-channel:1")
}

func TestLoopRefreshFailureServesRetainedArchive(t *testing.T) {
	e, id := seedLoopFault(t)
	stubLoopSync(t, func(call int, opts syncer.LoopOptions) (syncer.LoopReport, error) {
		if opts.MaxAge > 0 {
			return syncer.LoopReport{}, errors.New("synthetic refresh failure")
		}
		return syncer.SyncLoop(context.Background(), opts)
	})
	for _, args := range [][]string{{"loop", "list"}, {"loop", "show", id}, {"search", "synthetic", "--source", "loop"}, {"search", "synthetic"}} {
		doc := decode(t, runOK(t, e, args...))
		refresh, _ := doc["loop_coverage"].(map[string]any)["refresh_error"].(map[string]any)
		if refresh == nil || refresh["code"] != "db_error" || len(loopCoverageSources(t, doc)) != 1 {
			t.Fatalf("%v: retained result lacks refresh_error: %v", args, doc["loop_coverage"])
		}
	}
	text := runOK(t, e, "--format", "text", "loop", "list")
	if !strings.Contains(text, "loop refresh error: db_error") || !strings.Contains(text, "latest cached snapshot") {
		t.Fatalf("text refresh error:\n%s", text)
	}

	stubLoopSync(t, func(int, syncer.LoopOptions) (syncer.LoopReport, error) {
		return syncer.LoopReport{}, errors.New("synthetic archive failure")
	})
	wantCode(t, e, 1, "db_error", "loop", "list")
	wantCode(t, e, 1, "db_error", "loop", "sources")
	wantCode(t, e, 1, "db_error", "search", "synthetic")

	stubLoopSync(t, func(int, syncer.LoopOptions) (syncer.LoopReport, error) {
		return syncer.LoopReport{}, context.Canceled
	})
	if code, stdout, _ := e.run("loop", "list"); code == 0 || stdout != "" {
		t.Fatalf("cancelled refresh served a result: %d %s", code, stdout)
	}
}

func TestLoopReadFaults(t *testing.T) {
	e, id := seedLoopFault(t)
	// The channel resolves, then its table breaks during the refresh: the reread fails closed.
	stubLoopSync(t, func(int, syncer.LoopOptions) (syncer.LoopReport, error) {
		breakLoopChannels(t, e)
		return syncer.LoopReport{}, nil
	})
	wantCode(t, e, 1, "db_error", "loop", "show", id)
	stubLoopSync(t, func(int, syncer.LoopOptions) (syncer.LoopReport, error) { return syncer.LoopReport{}, nil })
	wantCode(t, e, 1, "db_error", "loop", "list")
	wantCode(t, e, 1, "db_error", "search", "synthetic", "--source", "loop")
	wantCode(t, e, 1, "db_error", "--max-age", "0", "search", "synthetic")

	if err := os.WriteFile(e.db, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e, 1, "db_error", "loop", "list")
	wantCode(t, e, 1, "db_error", "loop", "show", id)
}

func TestLoopTextAndCoverageShapes(t *testing.T) {
	e, _ := seedLoopFault(t)
	text := runOK(t, e, "--format", "text", "loop", "sync")
	if !strings.Contains(text, "loop: 0 projected, 1 unchanged, 0 skipped, 0 failed") || !strings.Contains(text, "loop coverage: registered") {
		t.Fatalf("text loop sync:\n%s", text)
	}
	at := time.Date(2026, 10, 12, 1, 0, 0, 0, time.FixedZone("synthetic", 3600))
	item := loopChannelItemOf(store.LoopChannelRow{ID: 7, RawUpdatedAt: &at, Retained: true,
		Channel:  loopcache.Channel{Path: "channels/long", Text: "synthetic text"},
		Snapshot: loopcache.Result{Losses: []loopcache.Loss{{Code: "synthetic_loss", Count: 2}, {Code: "synthetic_loss", Count: 1}}}}, 5)
	if item.Losses["synthetic_loss"] != 3 || !item.TextTruncated || !item.PathTruncated || item.RawUpdatedAt.Location() != time.UTC ||
		item.ModifiedAt != nil || loopRowState(item) != "older cached snapshot, archived record differs, archived record gone from the app's cache" {
		t.Fatalf("channel item: %+v %s", item, loopRowState(item))
	}
	failed := store.LoopSource{Registration: store.LoopRegistration{ID: 4}, LastSnapshotID: 9,
		Health: store.KnowledgeSource{Status: "failed", Reason: "loop_cache_input_changed"}}
	coverage := (&runtime{}).loopCoverageOf([]store.LoopSource{failed}, []string{"synthetic diagnostic"})
	if coverage.State != "registered" || !strings.Contains(coverage.items[0].Effect, "last good snapshot stays the latest cached snapshot") ||
		!strings.Contains(coverage.items[0].Fix, "loop sync --source-id 4") || coverage.Diagnostics[0] != "synthetic diagnostic" {
		t.Fatalf("failed coverage: %+v", coverage)
	}
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	rt.renderLoopCoverage(nil)
	rt.renderLoopCoverage(rt.loopCoverageOf(nil, nil))
	if out.Len() != 0 {
		t.Fatalf("empty coverage rendered: %s", out.String())
	}
	rt.renderLoopCoverage(coverage)
	if got := out.String(); !strings.Contains(got, "loop diagnostic: synthetic diagnostic") || !strings.Contains(got, "fix: ") {
		t.Fatalf("rendered coverage:\n%s", got)
	}
}
