//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestOWACompleteImportDiagnosticPaths(t *testing.T) {
	for mode, reason := range map[string]string{
		"sync": "sync_refused", "source_missing": "sync_source_refused", "source_multiple": "sync_source_refused",
		"calendar_source": "sync_source_refused", "calendar_error": "sync_source_refused", "calendar_omissions": "sync_source_refused", "calendar_status": "sync_status_mismatch",
		"omissions": "sync_source_refused", "product_omissions": "sync_source_refused", "unknown_omissions": "unproved", "status": "sync_status_mismatch", "counts": "sync_counts_mismatch",
		"archive_open": "archive_open_failed", "state": "archive_state_refused", "state_budget": "archive_state_budget",
		"query_budget": "query_projection_budget", "repeat": "repeat_archive_unstable", "mismatch": "archive_mismatch",
		"global": "account_isolation_mismatch", "stability": "unstable_source_observation", "cleanup": "archive_cleanup_failed", "close_failure": "archive_close_failed",
	} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(owaScratch(t), "import-diagnostic.json")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("synthetic_artifact_failed")
			}
			// #nosec G204 G702 -- Exact current artifact, fixed synthetic-only probe; no child stdout/stderr capture.
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestOWACompleteImportDiagnosticProbe$")
			cmd.Env = append(os.Environ(), "M365CRAWL_REAL_CACHE=0", "OWA_IMPORT_PROBE="+mode, "OWA_IMPORT_PATH="+path)
			if cmd.Run() == nil {
				t.Fatal("synthetic_failed_check_passed")
			}
			// #nosec G304 G703 -- Exact private counts/fixed-code-only probe record.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("first_failpoint_witness_missing")
			}
			var actual struct {
				Stage, Reason string
				Fields        []string
				Omissions     map[string]int64
			}
			if json.Unmarshal(data, &actual) != nil {
				t.Fatal("synthetic_failure_witness_invalid")
			}
			stage := protocol.StageImport
			switch mode {
			case "global":
				stage = protocol.StageAccounts
			case "stability":
				stage = protocol.StageStability
			case "cleanup", "close_failure":
				stage = protocol.StageCleanup
			}
			if actual.Stage != stage || actual.Reason != reason || !protocol.DiagnosticFields(actual.Fields) || !protocol.DiagnosticOmissions(actual.Stage, actual.Reason, actual.Omissions) {
				t.Fatal("first_responsible_failpoint_mismatch")
			}
			if mode == "omissions" && !reflect.DeepEqual(actual.Omissions, map[string]int64{"outlook_owa_detail_conflict": 2}) {
				t.Fatal("fixed_product_loss_count_missing")
			}
			if mode == "unknown_omissions" && len(actual.Omissions) != 0 {
				t.Fatal("unknown_product_loss_exposed")
			}
			if mode == "product_omissions" && actual.Omissions["outlook_owa_detail_conflict"] != 1 {
				t.Fatal("actual_product_conflict_count_missing")
			}
		})
	}
}

func TestOWACompleteImportDiagnosticProbe(t *testing.T) {
	mode := os.Getenv("OWA_IMPORT_PROBE")
	if mode == "" {
		t.Skip("synthetic-only complete-import diagnostic subprocess")
	}
	if os.Getenv("M365CRAWL_REAL_CACHE") != "0" {
		t.Fatal("synthetic_real_flag_refused")
	}
	s := &owaFailureState{}
	var cleanupLeaf, cleanupDir string
	// Synthetic cleanup refusal is repaired after its failure witness; the actual selected-test cleanup order remains unchanged.
	t.Cleanup(func() {
		if cleanupLeaf != "" {
			if os.Remove(cleanupLeaf) != nil || os.Remove(cleanupDir) != nil {
				t.Error("synthetic_cleanup_repair_failed")
			}
		}
	})
	owaFailureCleanup(t, s, func() error { return nil }, func(stage, reason string, fields []string, omissions map[string]int64) error {
		data, err := json.Marshal(struct {
			Stage, Reason string
			Fields        []string
			Omissions     map[string]int64
		}{stage, reason, fields, omissions})
		if err != nil {
			return errors.New("synthetic_encode_failed")
		}
		// #nosec G703 -- Parent-owned exact synthetic fixed-code/count scratch receipt.
		return os.WriteFile(os.Getenv("OWA_IMPORT_PATH"), data, 0o600)
	})
	// Later cleanup must not replace the first responsible failure.
	t.Cleanup(func() {
		s.cleanup(t, func() { s.record(protocol.StageCleanup, "archive_cleanup_failed"); t.Error("archive_cleanup_failed") })
	})
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"), s)
	path := filepath.Join(owaScratch(t), "import.db")
	defer s.phase(t, protocol.StageImport)()
	if mode == "sync" {
		ctx, cancel := context.WithCancel(c.ctx)
		cancel()
		c.ctx = ctx
		owaExercise(t, path, c)
		return
	}
	if mode == "product_omissions" {
		// Negative source mutation occurs after the independent census: exercise actual product loss reporting, never claim source stability or qualification for this copy.
		owaSyntheticTable(t, c, owaObject("ItemId", owaIdentity("Message/Normal=="), "UID", "conflicting-synthetic"), false)
		owaExercise(t, path, c)
		return
	}
	// Use a real normal synthetic report, changing only controlled test-owned report fields for each existing guard.
	rep, _, err := syncer.Run(c.ctx, syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: path, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1})
	mailIndex := -1
	for i, row := range rep.Sources {
		if row.Source == "outlook|"+c.profile.Name+"|mail" {
			mailIndex = i
		}
	}
	if err != nil || len(rep.Sources) != 2 || mailIndex < 0 || rep.Sources[mailIndex].Counts == nil || rep.Sources[mailIndex].Counts.Mail == nil {
		t.Fatal("synthetic_import_setup_failed")
	}
	switch mode {
	case "source_missing", "source_multiple", "omissions", "unknown_omissions", "status", "counts", "calendar_source", "calendar_error", "calendar_omissions", "calendar_status":
		switch mode {
		case "source_missing":
			rep.Sources = nil
		case "source_multiple":
			rep.Sources = append(rep.Sources, rep.Sources[mailIndex])
		case "omissions":
			rep.Sources[mailIndex].Omissions = map[string]int{"outlook_owa_detail_conflict": 2}
			rep.Sources[mailIndex].Status = syncer.StatusOmissions
		case "unknown_omissions":
			rep.Sources[mailIndex].Omissions = map[string]int{"private-unknown-never-output": 1}
		case "status":
			rep.Sources[mailIndex].Status = syncer.StatusUnchanged
		case "counts":
			rep.Sources[mailIndex].Counts.Mail.Added++
		case "calendar_source":
			rep.Sources[1-mailIndex].Source = "unexpected-synthetic-calendar"
		case "calendar_error":
			rep.Sources[1-mailIndex].Error = &syncer.SourceError{Code: "outlook_layout_unsupported"}
		case "calendar_omissions":
			rep.Sources[1-mailIndex].Omissions = map[string]int{"outlook_event_unmapped": 1}
		case "calendar_status":
			rep.Sources[1-mailIndex].Status = syncer.StatusUnchanged
		}
		owaSyncChecks(t, rep, nil, c, 0)
	case "archive_open":
		owaArchive(t, filepath.Join(owaScratch(t), "missing.db"), s)
	case "state", "state_budget", "query_budget", "repeat", "mismatch", "global", "stability", "cleanup", "close_failure":
		st := owaArchive(t, path, s)
		switch mode {
		case "state":
			ctx, cancel := context.WithCancel(c.ctx)
			cancel()
			owaArchiveState(ctx, t, st, s)
		case "state_budget":
			owaArchiveStateLimit(c.ctx, t, st, 1, s)
		case "query_budget":
			_, err := owaQueryDiagnosticBudget(c.ctx, st, `select subject from mail_messages`, 1, s)
			if err == nil {
				t.Fatal("synthetic_projection_refusal_missing")
			}
			owaArchiveFindings(t, s, []string{"archive_query"})
		case "repeat":
			baseline := owaArchiveState(c.ctx, t, st, s)
			owaCloseArchive(t, st)
			owaCorrupt(t, path, `update mail_messages set subject=subject||'changed'`)
			current := owaArchive(t, path, s)
			owaRepeatState(t, s, owaArchiveState(c.ctx, t, current, s), baseline)
		case "mismatch":
			owaCloseArchive(t, st)
			owaCorrupt(t, path, `update mail_messages set subject=subject||'changed'`)
			current := owaArchive(t, path, s)
			owaArchiveFindings(t, s, owaCompareContext(c.ctx, current, c.account, c.census, s))
		case "global":
			bad := c
			bad.account = "unexpected-synthetic"
			owaGlobalAccounts(t, st, []owaObservation{bad}, s)
		case "stability":
			owaSourceStability(t, 1, s)
		case "cleanup":
			root := owaScratch(t, s)
			cleanupDir = filepath.Join(root, "synthetic-nonflat")
			cleanupLeaf = filepath.Join(cleanupDir, "owned")
			if os.Mkdir(cleanupDir, 0o700) != nil {
				t.Fatal("synthetic_cleanup_poison_failed")
			}
			// #nosec G703 -- Exact test-owned leaf; removed individually after the fixed cleanup refusal witness.
			if os.WriteFile(cleanupLeaf, []byte{}, 0o600) != nil {
				t.Fatal("synthetic_cleanup_poison_failed")
			}
			s.completed = true
		case "close_failure":
			// Store.Close is idempotent; exercise the same explicit close-failure recorder with a fixed synthetic cleanup failure.
			t.Cleanup(func() {
				s.cleanup(t, func() { s.record(protocol.StageCleanup, "archive_close_failed"); t.Error("archive_close_failed") })
			})
			s.completed = true
		}
	default:
		t.Fatal("synthetic_mode_refused")
	}
}
