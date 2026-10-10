//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestOWAFirstCleanupTupleSurvivesActualComparator(t *testing.T) {
	path := filepath.Join(owaScratch(t), "first-tuple.json")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_artifact_failed")
	}
	// #nosec G204 G702 -- Exact current artifact, fixed synthetic-only probe, child stdout/stderr discarded.
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestOWAFirstCleanupTupleProbe$")
	cmd.Env = append(os.Environ(), "M365CRAWL_REAL_CACHE=0", "OWA_FIRST_TUPLE_PATH="+path)
	if cmd.Run() == nil {
		t.Fatal("synthetic_failed_checks_passed")
	}
	// #nosec G304 G703 -- Exact private fixed-code/count-only probe receipt.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("first_failure_tuple_missing")
	}
	var actual struct {
		Stage, Reason string
		Fields        []string
		Omissions     map[string]int64
	}
	if json.Unmarshal(data, &actual) != nil || actual.Stage != protocol.StageCleanup || actual.Reason != "archive_close_failed" || len(actual.Fields) != 0 || len(actual.Omissions) != 0 {
		t.Fatal("first_cleanup_tuple_overwritten_by_later_comparator")
	}
}

func TestOWAFirstCleanupTupleProbe(t *testing.T) {
	path := os.Getenv("OWA_FIRST_TUPLE_PATH")
	if path == "" {
		t.Skip("synthetic-only first-tuple subprocess")
	}
	if os.Getenv("M365CRAWL_REAL_CACHE") != "0" {
		t.Fatal("synthetic_real_flag_refused")
	}
	s := &owaFailureState{}
	owaFailureCleanup(t, s, func() error { return nil }, func(stage, reason string, fields []string, omissions map[string]int64) error {
		data, err := json.Marshal(struct {
			Stage, Reason string
			Fields        []string
			Omissions     map[string]int64
		}{stage, reason, fields, omissions})
		if err != nil {
			return err
		}
		// #nosec G703 -- Exact parent-owned synthetic tuple receipt.
		return os.WriteFile(path, data, 0o600)
	})
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"), s)
	db := filepath.Join(owaScratch(t, s), "tuple.db")
	owaExercise(t, db, c)
	st := owaArchive(t, db, s)
	bad := c.census.clone()
	for id, m := range bad.messages {
		m.fields = append([]any{}, m.fields...)
		m.fields[3] = "synthetic-comparator-mismatch"
		bad.messages[id] = m
		break
	}
	defer s.phase(t, protocol.StageImport)()
	findings := owaCompareContext(c.ctx, st, c.account, bad, s)
	if len(findings) == 0 {
		t.Fatal("actual_comparator_mutation_not_detected")
	}
	// Model the approved fixed close-error recorder, not a real SQLite close fault.
	s.cleanup(t, func() {
		s.record(protocol.StageCleanup, "archive_close_failed")
		t.Error("archive_close_failed")
	})
	owaArchiveFindings(t, s, findings)
}

func TestOWAFirstTupleQueryBudgetOwnsFields(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
	path := filepath.Join(owaScratch(t), "query-tuple.db")
	owaExercise(t, path, c)
	st := owaArchive(t, path)
	for _, stage := range []string{protocol.StageImport, protocol.StageAccounts} {
		s := &owaFailureState{stage: stage}
		_, err := owaQueryDiagnosticBudget(context.Background(), st, `select subject from mail_messages`, 1, s)
		if err == nil || s.failure != stage || s.reason != "query_projection_budget" || !reflect.DeepEqual(s.fields, []string{"archive_query"}) {
			t.Fatal("responsible_query_budget_fields_missing")
		}
		s.recordFields(stage, "archive_mismatch", []string{"private-never-output"})
		s.sourceOmissions(map[string]int{"outlook_owa_detail_conflict": 1})
		if !reflect.DeepEqual(s.fields, []string{"archive_query"}) || s.reason != "query_projection_budget" || len(s.omissions) != 0 {
			t.Fatal("later_event_poisoned_query_tuple")
		}
	}
}

func TestOWAFirstTupleRejectsLaterFieldsAndOmissions(t *testing.T) {
	s := &owaFailureState{}
	s.sourceOmissions(map[string]int{"outlook_owa_detail_conflict": 2})
	s.recordFields(protocol.StageAccounts, "account_isolation_mismatch", []string{"private-never-output"})
	s.record(protocol.StageCleanup, "archive_close_failed")
	if s.failure != protocol.StageImport || s.reason != "sync_source_refused" || len(s.fields) != 0 || !reflect.DeepEqual(s.omissions, map[string]int64{"outlook_owa_detail_conflict": 2}) {
		t.Fatal("first_omission_tuple_overwritten")
	}
	for _, tc := range []struct{ stage, reason, field string }{
		{protocol.StageCensus, "detail_metadata_coverage_refused", "UID"},
		{protocol.StageAccounts, "account_isolation_mismatch", "archive_accounts"},
		{protocol.StageImport, "archive_mismatch", "subject"},
	} {
		s := &owaFailureState{}
		fields := []string{tc.field}
		s.recordFields(tc.stage, tc.reason, fields)
		fields[0] = "private-never-output"
		s.recordFields(tc.stage, tc.reason, []string{"private-never-output"})
		if s.failure != tc.stage || s.reason != tc.reason || !reflect.DeepEqual(s.fields, []string{tc.field}) || len(s.omissions) != 0 {
			t.Fatal("first_event_fields_not_immutable_copy")
		}
	}
}
