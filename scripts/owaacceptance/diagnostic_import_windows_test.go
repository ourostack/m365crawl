package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestImportDiagnosticSafeOmissionPacket(t *testing.T) {
	d := protocol.Diagnostic{Stage: protocol.StageImport, Reason: "sync_source_refused", Fields: []string{}, Omissions: map[string]int64{"outlook_owa_detail_conflict": 2}}
	packet := failurePacket(diagnosticWitnessFailure(d, errors.New("private-path-and-payload-never-output")))
	if packet != `owa_acceptance status=unqualified stage=import_repeat_compare reason=sync_source_refused fields=[] omissions={"outlook_owa_detail_conflict":2}` {
		t.Fatal("fixed_product_omissions_packet_missing")
	}
	for _, omissions := range []map[string]int64{
		nil, {"private-never-output": 1}, {"outlook_owa_detail_conflict": -1},
		{"outlook_owa_detail_conflict": protocol.MaxCount + 1},
	} {
		d.Omissions = omissions
		packet := failurePacket(diagnosticWitnessFailure(d, errors.New("private-never-output")))
		if packet != "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]" || strings.Contains(packet, "private") {
			t.Fatal("invalid_omission_packet_not_unproved")
		}
	}
	d.Omissions = map[string]int64{"outlook_owa_detail_conflict": 1}
	d.Reason = "sync_counts_mismatch"
	if failurePacket(diagnosticWitnessFailure(d, errors.New("private"))) != "owa_acceptance status=unqualified stage=unproved reason=unproved fields=[]" {
		t.Fatal("omission_wrong_reason_packet_accepted")
	}
}
