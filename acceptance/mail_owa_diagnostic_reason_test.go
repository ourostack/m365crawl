//go:build acceptance

package acceptance

import (
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestOWADiagnosticReasonPreservedBeforeFatal(t *testing.T) {
	s := &owaFailureState{}
	owaRecordCensusReason(s, errors.New("census_layout_refused"))
	if s.reason != "census_layout_refused" {
		t.Fatal("responsible_census_reason_missing")
	}

	s.failure = protocol.StageCensus
	s.phase(t, protocol.StageImport)()
	s.cleanup(t, func() {})
	if s.reason != "census_layout_refused" || s.failure != protocol.StageCensus {
		t.Fatal("nested_phase_reason_lost")
	}
	unknown := &owaFailureState{}
	owaRecordCensusReason(unknown, errors.New("private-value-never-output"))
	if unknown.reason != "unproved" {
		t.Fatal("private_census_reason_exposed")
	}
}

func TestOWAImportDiagnosticFirstFailureAndUnknownLossPrivacy(t *testing.T) {
	s := &owaFailureState{}
	s.record(protocol.StageImport, "sync_counts_mismatch")
	s.sourceOmissions(map[string]int{"outlook_owa_detail_conflict": 2})
	s.phase(t, protocol.StageAccounts)()
	s.cleanup(t, func() { s.record(protocol.StageCleanup, "archive_close_failed") })
	if s.failure != protocol.StageImport || s.reason != "sync_counts_mismatch" || len(s.omissions) != 0 {
		t.Fatal("first_import_reason_overwritten")
	}
	for _, omissions := range []map[string]int{
		{"private-never-output": 1}, {"outlook_owa_detail_conflict": -1},
		{"outlook_owa_detail_conflict": 0},
		{"outlook_owa_detail_conflict": protocol.MaxCount, "outlook_owa_root_uncertain": 1},
	} {
		s := &owaFailureState{}
		s.sourceOmissions(omissions)
		if s.failure != protocol.StageImport || s.reason != "unproved" || len(s.omissions) != 0 {
			t.Fatal("untrusted_product_loss_diagnostic_exposed")
		}
	}
	input := map[string]int{"outlook_owa_detail_conflict": 2}
	copied := &owaFailureState{}
	copied.sourceOmissions(input)
	input["outlook_owa_detail_conflict"] = 3
	if copied.reason != "sync_source_refused" || copied.omissions["outlook_owa_detail_conflict"] != 2 {
		t.Fatal("source_report_omissions_not_copied")
	}
}
