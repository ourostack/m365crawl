//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

type owaFailureState struct {
	stage     string
	failure   string
	reason    string
	fields    []string
	omissions map[string]int64
	completed bool
}

func owaRecordCensusReason(s *owaFailureState, err error) {
	if s != nil && s.failure == "" {
		s.reason = protocol.CensusReason(err)
	}
}

func (s *owaFailureState) phase(t *testing.T, stage string) func() {
	if s == nil {
		return func() {}
	}
	if s.failure == "" && !t.Failed() {
		s.stage = stage
		s.reason = ""
		s.fields = nil
		s.omissions = nil
	}
	return func() {
		if t.Failed() && s.failure == "" {
			s.failure = stage
		}
	}
}

func (s *owaFailureState) record(stage, reason string) {
	s.recordFields(stage, reason, nil)
}

func (s *owaFailureState) recordFields(stage, reason string, fields []string) {
	if s != nil && s.failure == "" {
		s.failure = stage
		s.reason = protocol.FixedDiagnosticReason(stage, reason)
		s.fields = append([]string{}, fields...)
		s.omissions = map[string]int64{}
	}
}

func (s *owaFailureState) sourceOmissions(omissions map[string]int) {
	if s == nil || s.failure != "" {
		return
	}
	s.record(protocol.StageImport, "sync_source_refused")
	safe, valid := protocol.DiagnosticSourceOmissions(omissions)
	if !valid {
		s.reason = "unproved"
		s.omissions = map[string]int64{}
		return
	}
	s.omissions = safe
}
func owaFailureOf(states []*owaFailureState) *owaFailureState {
	if len(states) > 0 {
		return states[0]
	}
	return nil
}

func owaGlobalAccounts(t *testing.T, st *store.Store, observations []owaObservation, s *owaFailureState) {
	t.Helper()
	defer s.phase(t, protocol.StageAccounts)()
	if fields := owaCompareAllContext(owaWorkContext(t), st, observations, s); len(fields) != 0 {
		s.recordFields(protocol.StageAccounts, "account_isolation_mismatch", fields)
		t.Fatal("account_isolation_mismatch")
	}

}

func owaSourceStability(t *testing.T, unstable int, s *owaFailureState) {
	t.Helper()
	defer s.phase(t, protocol.StageStability)()
	if unstable != 0 {
		s.record(protocol.StageStability, "unstable_source_observation")
		t.Error("unstable_source_observation")
	}
}
func (s *owaFailureState) cleanup(t *testing.T, fn func()) {
	before := t.Failed()
	fn()
	if s != nil && !before && t.Failed() && s.failure == "" {
		s.failure = protocol.StageCleanup
		s.fields = []string{}
		s.reason = "unproved"
		s.omissions = nil
	}
}
func owaFailureCleanup(t *testing.T, s *owaFailureState, success func() error, failure func(string, string, []string, map[string]int64) error) {
	t.Cleanup(func() {
		if t.Failed() {
			stage := s.failure
			if stage == "" {
				stage = s.stage
			}
			if s.completed && s.failure == "" {
				stage = protocol.StageCleanup
			}
			fields := s.fields
			if fields == nil {
				fields = []string{}
			}
			reason := s.reason
			if reason == "" {
				reason = "unproved"
			}
			omissions := s.omissions
			if omissions == nil {
				omissions = map[string]int64{}
			}
			if !protocol.DiagnosticStage(stage) || !protocol.DiagnosticReason(stage, reason) || !protocol.DiagnosticFields(fields) || !protocol.DiagnosticOmissions(stage, reason, omissions) || failure(stage, reason, fields, omissions) != nil {
				t.Error("diagnostic_write_refused")
			}
		} else if success() != nil {
			t.Error("qualification_witness_refused")
		}
	})
}

func TestOWADiagnosticResponsibleStages(t *testing.T) {
	for _, mode := range []string{"capture", "integrity", "census", "conflict", "import", "global", "stability", "cleanup", "failed_write", "success"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(owaScratch(t), "diagnostic-probe")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("synthetic_artifact_failed")
			}
			// #nosec G204 G702 -- Exact current acceptance artifact, fixed synthetic-only probe; child output remains discarded.
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestOWADiagnosticStageProbe$")
			cmd.Env = append(os.Environ(), "M365CRAWL_REAL_CACHE=0", "OWA_DIAGNOSTIC_PROBE="+mode, "OWA_DIAGNOSTIC_PATH="+path)
			err = cmd.Run()
			if mode == "failed_write" {
				if err == nil {
					t.Fatal("failed_diagnostic_write_passed")
				}
				return
			}
			if mode == "success" {
				if err != nil {
					t.Fatal("positive_probe_failed")
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("positive_emitted_failure_diagnostic")
				}
				return
			}
			if err == nil {
				t.Fatal("failure_probe_passed")
			}
			// #nosec G304 G703 -- Exact private synthetic fixed-stage probe record.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal("failure_probe_record_missing")
			}
			var actual struct {
				Stage  string
				Reason string
				Fields []string
			}
			if json.Unmarshal(data, &actual) != nil {
				t.Fatal("failure_probe_record_invalid")
			}
			want := map[string]string{"capture": protocol.StageCapture, "integrity": protocol.StageCapture, "census": protocol.StageCensus, "conflict": protocol.StageCensus, "import": protocol.StageImport, "global": protocol.StageAccounts, "stability": protocol.StageStability, "cleanup": protocol.StageCleanup}[mode]
			if actual.Stage != want {
				t.Fatal("responsible_failure_stage_mismatch")
			}
			if mode == "conflict" && (len(actual.Fields) != 1 || actual.Fields[0] != "UID") {
				t.Fatal("conflict_field_witness_missing")
			}
			if mode == "conflict" && actual.Reason != "detail_metadata_coverage_refused" {
				t.Fatal("conflict_fixed_reason_missing")
			}
			remaining := map[string]string{"import": "sync_refused", "global": "account_isolation_mismatch", "stability": "unstable_source_observation", "integrity": "private_source_mutated"}
			if wantReason, ok := remaining[mode]; ok && actual.Reason != wantReason {
				t.Fatal("remaining_responsible_reason_missing")
			}
		})
	}
}

func TestOWADiagnosticStageProbe(t *testing.T) {
	mode := os.Getenv("OWA_DIAGNOSTIC_PROBE")
	if mode == "" {
		t.Skip("synthetic-only diagnostic stage subprocess")
	}
	if os.Getenv("M365CRAWL_REAL_CACHE") != "0" {
		t.Fatal("synthetic_real_flag_refused")
	}
	s := &owaFailureState{}
	owaFailureCleanup(t, s, func() error { return nil }, func(stage, reason string, fields []string, _ map[string]int64) error {
		if mode == "failed_write" {
			return errors.New("synthetic_write_refused")
		}
		data, err := json.Marshal(struct {
			Stage  string
			Reason string
			Fields []string
		}{stage, reason, fields})
		if err != nil {
			return errors.New("synthetic_encode_failed")
		}
		// #nosec G703 -- Parent-owned exact synthetic scratch stage receipt.
		return os.WriteFile(os.Getenv("OWA_DIAGNOSTIC_PATH"), data, 0o600)
	})
	switch mode {
	case "capture":
		owaCapture(t, outlookowa.Profile{}, s)
	case "integrity":
		obs := owaCapture(t, owaFixtureProfile("mail", "Synthetic"), s)
		obs.digest[0] ^= 1
		owaCapturedIntegrity(t, &obs)
	case "census", "conflict":
		p := owaFixtureProfile("mail", "Synthetic")
		obs := owaCapture(t, p, s)
		if mode == "census" {
			if os.Remove(filepath.Join(obs.profile.Source.LevelDBDir, "CURRENT")) != nil {
				t.Fatal("synthetic_census_mutation_failed")
			}
		} else {
			owaSyntheticTable(t, obs, owaObject("ItemId", owaIdentity("Message/Normal=="), "UID", "conflicting-synthetic"), false)
		}
		owaCensusObserved(t, &obs)
	case "import":
		obs := owaObserve(t, owaFixtureProfile("mail", "Synthetic"), s)
		ctx, cancel := context.WithCancel(obs.ctx)
		cancel()
		obs.ctx = ctx
		owaExercise(t, filepath.Join(owaScratch(t), "import.db"), obs)
	case "global":
		obs := owaObserve(t, owaFixtureProfile("mail", "Synthetic"), s)
		db := filepath.Join(owaScratch(t), "global.db")
		owaExercise(t, db, obs)
		st := owaArchive(t, db, s)
		bad := obs
		bad.account = "unexpected-synthetic"
		owaGlobalAccounts(t, st, []owaObservation{bad}, s)
	case "cleanup":
		t.Cleanup(func() { s.cleanup(t, func() { t.Error("synthetic_cleanup_failed") }) })
		s.completed = true
	case "stability":
		owaSourceStability(t, 1, s)
	case "failed_write":
		defer s.phase(t, protocol.StageImport)()
		t.Fatal("synthetic_failed_test")
	case "success":
		s.completed = true
	default:
		t.Fatal("synthetic_mode_refused")
	}
}
