package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestOWAMailRetentionNoticePreservesCLIPrimaryError(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		var out, stderr bytes.Buffer
		rt := newRuntime(context.Background(), &Globals{}, &out, &stderr)
		rt.format = output.JSON
		primary := error(&errs.Coded{Code: "synthetic_layout_refused", Message: "fixed source refusal", Fix: "fixed remedy", Exit: errs.ExitEnvironment})
		wantCode, wantExit := "synthetic_layout_refused", errs.ExitEnvironment
		if canceled {
			primary, wantCode, wantExit = context.Canceled, errs.CodeInterrupted, errs.ExitRuntime
		}
		got := rt.fail(errors.Join(primary, syncer.ErrOWASnapshotRetained))
		body := errorOf(t, stderr.String())
		if got != wantExit || body["code"] != wantCode || !strings.Contains(body["message"].(string), syncer.ErrOWASnapshotRetained.Error()) {
			t.Fatal("CLI lost retention information or changed its primary code/exit")
		}
		if !canceled && body["fix"] != "fixed remedy" {
			t.Fatal("retention notice replaced the primary remedy")
		}
	}
}

func TestOWAMailCanceledSyncCommandReportsRetainedSnapshot(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := runSync
	t.Cleanup(func() { runSync = old })
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		cancel()
		return syncer.Report{Status: syncer.StatusFailed}, nil, errors.Join(context.Canceled, syncer.ErrOWASnapshotRetained)
	}
	var out, stderr bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--json", "sync"}, &out, &stderr)
	body := errorOf(t, stderr.String())
	if code != errs.ExitRuntime || body["code"] != errs.CodeInterrupted ||
		!strings.Contains(body["message"].(string), syncer.ErrOWASnapshotRetained.Error()) || out.Len() != 0 {
		t.Fatal("the public sync command discarded retention or changed interruption semantics")
	}
}

func TestOWAMailCanceledWatchWarnsWithoutChangingSuccessfulStop(t *testing.T) {
	noEvents(t)
	w := newWatchEnv(t)
	old := runSync
	t.Cleanup(func() { runSync = old })
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		w.cancel()
		return syncer.Report{Status: syncer.StatusFailed}, nil, errors.Join(context.Canceled, syncer.ErrOWASnapshotRetained)
	}
	w.start("watch", "--every", "1h")
	select {
	case code := <-w.done:
		if code != 0 || !strings.Contains(w.errb.String(), syncer.ErrOWASnapshotRetained.Error()) ||
			len(kinds(w.out.lines(t), "error")) != 0 {
			t.Fatal("a canceled watch hid its retained snapshot or became a terminal failure")
		}
	case <-time.After(watchTestWaitTimeout):
		t.Fatal("canceled watch did not stop")
	}
}
