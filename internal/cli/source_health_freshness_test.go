package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestSourceHealthNeedsSyncDoesNotClaimHealthy(t *testing.T) {
	m := meta{NeedsSync: true}
	m.setSourceHealth([]store.SourceHealth{})
	if m.SourceHealth != nil {
		t.Fatal("needs_sync result claimed checked healthy sources")
	}
	m.setSourceHealth([]store.SourceHealth{{Source: "teams", State: "never_succeeded"}})
	if m.SourceHealth == nil || len(*m.SourceHealth) != 1 {
		t.Fatal("needs_sync hid a recorded first-sync failure")
	}
}

func TestSourceHealthReflectsImplicitRefresh(t *testing.T) {
	e := newEnv(t)
	e.sourceHealthArchive()
	old := runSync
	calls := 0
	runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
		calls++
		st, err := store.Open(ctx, o.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		if err := st.RecordRun(ctx, store.Run{Source: "synthetic|teams", Status: "failed", ErrorCode: "synthetic_refresh_failure", StartedAt: mailT0, FinishedAt: mailT0}); err != nil {
			t.Fatal(err)
		}
		return syncer.Report{Status: syncer.StatusOK}, nil, nil
	}
	t.Cleanup(func() { runSync = old })
	code, out, stderr := e.run("--json", "--max-age", "1ns", "messages", "--fields", "id")
	if code != 0 || calls != 1 || !strings.Contains(stderr, `"notice":"syncing"`) {
		t.Fatalf("synthetic refresh did not run: %d %d %q", code, calls, stderr)
	}
	doc := decode(t, out)
	health := doc["source_health"].([]any)
	if len(health) != 1 || health[0].(map[string]any)["last_error_code"] != "synthetic_refresh_failure" {
		t.Fatalf("health predates refresh: %v", health)
	}
}
