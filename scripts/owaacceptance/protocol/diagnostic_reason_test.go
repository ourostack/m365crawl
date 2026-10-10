package protocol

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDiagnosticReasonExactMappingAndStrictWire(t *testing.T) {
	for _, code := range []string{"census_layout_refused", "census_metadata_refused", "census_duplicate_field_refused", "body_derivation_encoding_unproved", "body_rawtext_semantics_unproved", "detail_metadata_coverage_refused", "aggregate_payload_budget"} {
		if CensusReason(errors.New(code)) != code {
			t.Error("fixed_census_reason_not_mapped")
		}
	}
	for _, err := range []error{nil, errors.New("private-source-never-output"), fmt.Errorf("private wrapper: %w", errors.New("census_layout_refused")), errors.New("census_layout_refused private"), errors.New("CENSUS_LAYOUT_REFUSED")} {
		if CensusReason(err) != "unproved" {
			t.Error("untrusted_reason_mapped")
		}
	}
	d := DiagnosticFor(validOwner(), StageCensus, []string{})
	d.Reason = "census_layout_refused"
	data, _ := Encode(d)
	if _, err := DecodeDiagnostic(data); err != nil {
		t.Fatal("known_census_reason_refused")
	}
	for _, reason := range []string{"", "private-source-never-output", "census_layout_refused private", "CENSUS_LAYOUT_REFUSED"} {
		d.Reason = reason
		wire, _ := Encode(d)
		if _, err := DecodeDiagnostic(wire); err == nil {
			t.Error("unknown_diagnostic_reason_accepted")
		}
	}
	d.Reason = "census_layout_refused"
	d.Stage = StageImport
	wire, _ := Encode(d)
	if _, err := DecodeDiagnostic(wire); err == nil {
		t.Error("census_reason_wrong_stage_accepted")
	}
	for _, bad := range []string{
		strings.Replace(string(data), `,"reason":"census_layout_refused"`, "", 1),
		strings.Replace(string(data), `"reason":"census_layout_refused"`, `"reason":"unproved","reason":"census_layout_refused"`, 1),
		strings.Replace(string(data), `"reason":"census_layout_refused"`, `"reason":null`, 1),
	} {
		if _, err := DecodeDiagnostic([]byte(bad)); err == nil {
			t.Error("missing_duplicate_null_reason_accepted")
		}
	}
}
