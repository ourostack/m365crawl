package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestOWAMailCanceledImplicitSyncReportsRetainedSnapshot(t *testing.T) {
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
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--json", "search", "synthetic"}, &out, &stderr)
	body := errorOf(t, stderr.String())
	if code != errs.ExitRuntime || body["code"] != errs.CodeInterrupted ||
		!strings.Contains(body["message"].(string), syncer.ErrOWASnapshotRetained.Error()) {
		t.Fatal("implicit refresh discarded its canceled snapshot-retention notice")
	}
}

func TestOWAMailDegradedImplicitSyncReportsRetainedSnapshot(t *testing.T) {
	e := newEnv(t)
	old := runSync
	t.Cleanup(func() { runSync = old })
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{Status: syncer.StatusFailed}, nil, errors.Join(
			&errs.Coded{Code: "synthetic_layout_refused", Message: "fixed source refusal", Fix: "fixed remedy", Exit: errs.ExitEnvironment},
			syncer.ErrOWASnapshotRetained)
	}
	var out, stderr bytes.Buffer
	rt := newRuntime(context.Background(), &Globals{}, &out, &stderr)
	rt.maxAge, rt.dbPath, rt.root, rt.format = defaultMaxAge, e.db, e.root, output.JSON
	problem, err := rt.ensureFresh()
	if err != nil || problem == nil || problem.Code != "synthetic_layout_refused" ||
		!strings.Contains(problem.Message, syncer.ErrOWASnapshotRetained.Error()) ||
		!strings.Contains(stderr.String(), syncer.ErrOWASnapshotRetained.Error()) {
		t.Fatal("degraded archive refresh hid snapshot retention or became a terminal failure")
	}
}

func TestOWAMailRetentionAnnotationIsNotDuplicated(t *testing.T) {
	primary := &errs.Coded{Code: "synthetic_refusal", Message: syncer.ErrOWASnapshotRetained.Error(), Fix: "fixed remedy", Exit: errs.ExitEnvironment}
	got := withSnapshotRetention(errors.Join(primary, syncer.ErrOWASnapshotRetained), primary)
	if strings.Count(got.Message, syncer.ErrOWASnapshotRetained.Error()) != 1 || got.Code != primary.Code || got.Fix != primary.Fix || got.Exit != primary.Exit {
		t.Fatal("a retention annotation was duplicated or changed the primary error")
	}
}
