package protocol

import (
	"strings"
	"testing"
)

func TestDiagnosticRemainingStageReasons(t *testing.T) {
	for stage, reasons := range map[string][]string{
		StageImport:    {"sync_refused", "sync_source_refused", "sync_status_mismatch", "sync_counts_mismatch", "archive_open_failed", "archive_state_refused", "archive_state_budget", "query_projection_budget", "repeat_archive_unstable", "archive_mismatch"},
		StageAccounts:  {"archive_open_failed", "account_isolation_mismatch", "query_projection_budget"},
		StageStability: {"unstable_source_observation"},
		StageCapture:   {"private_source_mutated", "payload_resource_or_context_refused", "payload_digest_failed"},
		StageCleanup:   {"snapshot_cleanup_failed", "archive_cleanup_failed", "archive_close_failed"},
	} {
		for _, reason := range reasons {
			if !DiagnosticReason(stage, reason) {
				t.Errorf("remaining_fixed_reason_refused stage=%s reason=%s", stage, reason)
			}
			if DiagnosticReason(StageCensus, reason) || DiagnosticReason(StageGuard, reason) {
				t.Error("remaining_reason_wrong_stage_accepted")
			}
		}
	}
}

func TestDiagnosticImportOmissionsStrictWire(t *testing.T) {
	d := DiagnosticFor(validOwner(), StageImport, []string{})
	d.Reason = "sync_source_refused"
	data, _ := Encode(d)
	// Raw wire constructs the new field without relying on a mirrored producer.
	base := strings.TrimSuffix(string(data), "}")
	if strings.Contains(base, `,"omissions":`) {
		base = strings.Split(base, `,"omissions":`)[0]
	}
	valid := base + `,"omissions":{"outlook_owa_detail_conflict":2,"outlook_owa_root_uncertain":1}}`
	if _, err := DecodeDiagnostic([]byte(valid)); err != nil {
		t.Fatal("fixed_product_omission_counts_refused")
	}
	for _, object := range []string{
		`null`, `[]`, `{"private-key":1}`, `{"outlook_owa_detail_conflict":-1}`,
		`{"outlook_owa_detail_conflict":0}`, `{"outlook_owa_detail_conflict":1.5}`,
		`{"outlook_owa_detail_conflict":1073741825}`, `{"outlook_owa_detail_conflict":null}`,
		`{"outlook_owa_detail_conflict":1,"outlook_owa_detail_conflict":2}`,
	} {
		if _, err := DecodeDiagnostic([]byte(base + `,"omissions":` + object + `}`)); err == nil {
			t.Error("invalid_omission_wire_accepted")
		}
	}
	for _, bad := range []string{
		strings.Replace(valid, `"stage":"import_repeat_compare"`, `"stage":"independent_census"`, 1),
		strings.Replace(valid, `"reason":"sync_source_refused"`, `"reason":"sync_counts_mismatch"`, 1),
		base + `}`,
		base + `,"omissions":{},"omissions":{}}`,
		strings.Replace(valid, `"outlook_owa_detail_conflict":2`, `"outlook_owa_detail_conflict":1073741824`, 1),
	} {
		if _, err := DecodeDiagnostic([]byte(bad)); err == nil {
			t.Error("missing_or_misbound_omissions_accepted")
		}
	}

}

func TestDiagnosticCentralReasonsPreserveExactCensusSet(t *testing.T) {
	if len(diagnosticReasons[StageCensus]) != 32 {
		t.Fatal("approved_census_reason_count_changed")
	}
	for stage, reasons := range diagnosticReasons {
		seen := map[string]bool{}
		for _, reason := range reasons {
			if seen[reason] || !DiagnosticReason(stage, reason) || FixedDiagnosticReason(stage, reason+" private") != "unproved" {
				t.Fatal("central_reason_table_not_exact")
			}
			seen[reason] = true
		}
	}
}
