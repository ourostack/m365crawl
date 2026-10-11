package main

import (
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestDiagnosticReasonSafeFailurePacket(t *testing.T) {
	err := diagnosticFailure(protocol.StageCensus, []string{}, errors.New("private-cause"), "census_layout_refused")
	if failurePacket(err) != "owa_acceptance status=unqualified stage=independent_census reason=census_layout_refused fields=[]" {
		t.Fatal("known_reason_safe_packet_missing")
	}
	for _, err := range []error{
		diagnosticFailure(protocol.StageCensus, []string{}, errors.New("private"), "private"),
		diagnosticFailure(protocol.StageImport, []string{}, errors.New("private"), "census_layout_refused"),
	} {
		if failurePacket(err) != "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]" {
			t.Fatal("unknown_or_mismatched_reason_exposed")
		}
	}
}
