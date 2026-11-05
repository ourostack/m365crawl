package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestReReviewB7ImplicitPersistenceFailureDoesNotClaimCheckedEmpty(t *testing.T) {
	e := newEnv(t)
	e.sync()
	old := runSync
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{
			Status: syncer.StatusPartial,
			Sources: []syncer.SourceReport{{
				Source: "synthetic|teams", Status: syncer.StatusFailed,
				Error: &syncer.SourceError{Code: "store_missing", Message: "Synthetic source read failed"},
			}},
			FailedSourceHealth: nil,
		}, nil, errs.DBError(errors.Join(errs.PartialSync("synthetic|teams (store_missing)"), errors.New("synthetic source-outcome persistence refused")))
	}
	t.Cleanup(func() { runSync = old })
	code, out, stderr := e.run("--json", "--max-age", "1ns", "messages")
	if code != 0 {
		if code != 1 || errorOf(t, stderr)["code"] != errs.CodeDBError {
			t.Fatalf("unexpected error outcome: code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		return
	}
	doc := decode(t, out)
	if !strings.Contains(stderr, "synthetic source-outcome persistence refused") || doc["sync_error"] == nil {
		t.Fatalf("reproduction did not expose the expected persistence warning: stdout=%s stderr=%s", out, stderr)
	}
	if health, checked := doc["source_health"].([]any); checked && len(health) == 0 {
		t.Fatalf("failed refresh with unpersisted source outcome claims source_health=[]: sync_error=%v", doc["sync_error"])
	}
}

func TestReReviewB7CancellationKeepsPersistenceDiagnostic(t *testing.T) {
	e := newEnv(t)
	old := runSync
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{Status: syncer.StatusPartial, FailedSourceHealth: nil}, nil,
			errs.DBError(errors.Join(context.Canceled, errors.New("synthetic source-outcome persistence refused")))
	}
	t.Cleanup(func() { runSync = old })
	code, out, stderr := e.run("--json", "sync")
	if code != 1 || out != "" {
		t.Fatalf("unexpected cancelled sync outcome: code=%d stdout=%s stderr=%s", code, out, stderr)
	}
	body := errorOf(t, stderr)
	if body["code"] != errs.CodeInterrupted || !strings.Contains(body["message"].(string), "synthetic source-outcome persistence refused") {
		t.Fatalf("public cancellation output lost the persistence error: %v", body)
	}
}
