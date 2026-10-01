package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// agedWatchEnv is a watch environment whose archive was synced and then left the way alpha.1 does:
// stale derived fields and hashes, and no derivation version.
func agedWatchEnv(t *testing.T) *watchEnv {
	t.Helper()
	w := newWatchEnv(t)
	noEvents(t)
	var out, errb bytes.Buffer
	if code := runCLI(context.Background(), []string{"--db", w.db, "--teams-root", w.root, "--json", "sync"}, &out, &errb); code != 0 {
		t.Fatalf("sync: %d %s", code, errb.String())
	}
	w.exec(`update messages set content_text='old', content_hash='old'`)
	w.exec(`delete from message_fts`)
	w.exec(`update conversations set content_hash='old'`)
	w.exec(`delete from meta`)
	return w
}

func TestWatchMigrationIsNotAnEdit(t *testing.T) {
	w := agedWatchEnv(t)
	// --emit-initial would print every change of the first sync, so any false edit shows.
	w.start("watch", "--every", "1h", "--emit-initial")
	w.waitFor("the sync line", func() bool { return len(kinds(w.out.lines(t), "sync")) > 0 })
	ls := w.out.lines(t)
	mig := kinds(ls, "migrated")
	if len(mig) != 1 || mig[0]["from"] != float64(1) || mig[0]["to"] != float64(2) || mig[0]["rows"].(float64) < 1 {
		t.Fatalf("want one migrated line, got %v", mig)
	}
	for _, kind := range []string{"message", "activity"} {
		if got := kinds(ls, kind); len(got) != 0 {
			t.Errorf("a migration emitted %d %s change lines, first %v", len(got), kind, got[0])
		}
	}
	rep := kinds(ls, "sync")[0]["report"].(map[string]any)
	if rep["messages"].(map[string]any)["updated"] != float64(0) || rep["migrated"] == nil {
		t.Errorf("sync report: %v", rep)
	}
	if w.archiveCount(`select count(*) from messages where content_text='old'`) != 0 {
		t.Error("the archive still holds the old text")
	}
}

func TestWatchMigrationTextLine(t *testing.T) {
	w := agedWatchEnv(t)
	w.start("watch", "--every", "1h", "--format", "text")
	w.waitFor("the migration line", func() bool { return strings.Contains(w.out.String(), "archive upgraded") })
	if strings.Contains(w.out.String(), "edited") {
		t.Errorf("text mode printed an edit: %q", w.out.String())
	}
}

func TestSyncReportsMigration(t *testing.T) {
	w := agedWatchEnv(t)
	sync := func(format string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := runCLI(context.Background(), []string{"--db", w.db, "--teams-root", w.root, "--format", format, "sync"}, &out, &errb); code != 0 {
			t.Fatalf("sync: %d %s", code, errb.String())
		}
		return out.String()
	}
	out := sync("json")
	var rep struct {
		Migrated *struct{ From, To, Rows int }
		Messages struct{ Updated int }
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Migrated == nil || rep.Migrated.Rows < 1 || rep.Messages.Updated != 0 {
		t.Fatalf("report %s (%v)", out, err)
	}
	if out := sync("json"); strings.Contains(out, "migrated") {
		t.Errorf("a current archive reported a migration: %s", out)
	}
	// The text report says so too.
	w.exec(`update messages set content_text='old', content_hash='old'`)
	w.exec(`delete from meta`)
	if out := sync("text"); !strings.Contains(out, "migrated: ") || !strings.Contains(out, "not counted as updates") {
		t.Errorf("text report: %s", out)
	}
}
