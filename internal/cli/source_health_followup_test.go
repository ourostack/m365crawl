package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
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

func TestSourceHealthImplicitUnknownKeepsRecordedFailureAndNormalWarning(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "established empty", true: "unestablished with recorded failure"}[known], func(t *testing.T) {
			e := newEnv(t)
			e.sync()
			if known {
				e.exec(`insert into sync_runs(started_at,source,status,error_code) values('2026-09-01','synthetic|teams','failed','store_missing')`)
			}
			old := runSync
			runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
				rep := syncer.Report{Status: syncer.StatusPartial, FailedSourceHealth: []store.SourceHealth{}}
				err := error(errs.PartialSync("synthetic source failure"))
				if known {
					rep.FailedSourceHealth = nil
					err = errs.DBError(errors.New("synthetic persistence refusal"))
				}
				return rep, nil, err
			}
			t.Cleanup(func() { runSync = old })
			code, out, stderr := e.run("--json", "--max-age", "1ns", "messages")
			if code != 0 || !strings.Contains(stderr, `"warning"`) {
				t.Fatalf("ordinary degraded read changed: %d %q %q", code, out, stderr)
			}
			doc := decode(t, out)
			health, present := doc["source_health"].([]any)
			want := 0
			if known {
				want = 1
			}
			if !present || len(health) != want || doc["sync_error"] == nil {
				t.Fatalf("recorded failure or checked normal health was lost: %v", doc)
			}
		})
	}
}

func TestSourceHealthCancellationDiagnosticAtImplicitAndTextBoundaries(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, implicit := range []bool{false, true} {
			for _, cancelContext := range []bool{false, true} {
				t.Run(format+map[bool]string{false: "/sync", true: "/implicit"}[implicit]+map[bool]string{false: "/wrapped-only", true: "/cancel-context"}[cancelContext], func(t *testing.T) {
					e := newEnv(t)
					if implicit {
						e.sync()
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					old := runSync
					runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
						if cancelContext {
							cancel()
						}
						return syncer.Report{Status: syncer.StatusPartial}, nil,
							errs.DBError(errors.Join(context.Canceled, errors.New("synthetic persistence refusal\nsynthetic diagnostic detail")))
					}
					t.Cleanup(func() { runSync = old })
					args := []string{"--db", e.db, "--teams-root", e.root, "--format", format, "sync"}
					if implicit {
						args = []string{"--db", e.db, "--teams-root", e.root, "--format", format, "--max-age", "1ns", "messages", "--limit", "1"}
					}
					var out, stderr bytes.Buffer
					code := runCLI(ctx, args, &out, &stderr)
					wantLines := 1
					if format == "text" {
						wantLines++
					}
					if implicit {
						wantLines++
					}
					lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
					if code != errs.ExitRuntime || out.Len() != 0 || len(lines) != wantLines ||
						!strings.Contains(stderr.String(), "synthetic persistence refusal") ||
						!strings.Contains(stderr.String(), "synthetic diagnostic detail") {
						t.Fatalf("cancellation lost detail or changed streams: code=%d stdout_bytes=%d stderr=%q", code, out.Len(), stderr.String())
					}
					if format == "json" {
						body := errorOf(t, lines[len(lines)-1])
						if body["code"] != errs.CodeInterrupted || !strings.HasPrefix(body["message"].(string), "interrupted before finishing; nothing was half-written") {
							t.Fatalf("interrupted contract changed: %v", body)
						}
					} else if !strings.Contains(stderr.String(), "error: interrupted before finishing; nothing was half-written") {
						t.Fatalf("ordinary interrupted message changed: %q", stderr.String())
					}
				})
			}
		}
	}
}
