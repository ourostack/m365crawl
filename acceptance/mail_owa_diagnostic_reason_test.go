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
