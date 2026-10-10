package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticStrictFailureIdentity(t *testing.T) {
	owner := validOwner()
	valid := DiagnosticFor(owner, StageCensus, []string{"ical_uid"})
	data, _ := Encode(valid)
	if _, err := DecodeDiagnostic(data); err != nil || MatchDiagnostic(owner, valid) != nil {
		t.Fatal("valid_diagnostic_refused")
	}
	for _, mutate := range []func(*Diagnostic){
		func(d *Diagnostic) { d.Stage = "private_untrusted" },
		func(d *Diagnostic) { d.Outcome = "passed" },
		func(d *Diagnostic) { d.Version++ },
		func(d *Diagnostic) { d.Test = "wrong" },
		func(d *Diagnostic) { d.Nonce = "bad" },
		func(d *Diagnostic) { d.BinaryHash = "bad" },
		func(d *Diagnostic) { d.RootNative = [3]uint32{} },
		func(d *Diagnostic) { d.Child = Process{} },
		func(d *Diagnostic) { d.Fields = []string{"private_field"} },
		func(d *Diagnostic) { d.Fields = []string{"ical_uid", "ical_uid"} },
		func(d *Diagnostic) { d.Fields = nil },
	} {
		d := DiagnosticFor(owner, StageCensus, []string{"ical_uid"})
		mutate(&d)
		bad, _ := Encode(d)
		if _, err := DecodeDiagnostic(bad); err == nil {
			t.Error("invalid_diagnostic_accepted")
		}
	}
	for _, mutate := range []func(*Diagnostic){
		func(d *Diagnostic) { d.Nonce = strings.Repeat("c", 64) },
		func(d *Diagnostic) { d.BinaryHash = strings.Repeat("c", 64) },
		func(d *Diagnostic) { d.Child.Created++ },
		func(d *Diagnostic) { d.Supervisor.Created++ },
		func(d *Diagnostic) { d.BinaryNative[2]++ },
		func(d *Diagnostic) { d.RootNative[2]++ },
	} {
		d := valid
		mutate(&d)
		if MatchDiagnostic(owner, d) == nil {
			t.Error("foreign_diagnostic_matched")
		}
	}
	initial := owner
	initial.Adopted = false
	initial.Child = Process{}
	if MatchDiagnostic(initial, valid) == nil {
		t.Error("preadoption_diagnostic_matched")
	}
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil {
		t.Fatal("synthetic_wire_failed")
	}
	delete(fields, "stage")
	missing, _ := json.Marshal(fields)
	for _, bad := range [][]byte{missing, append(append([]byte(nil), data...), []byte("{}")...), []byte(strings.Repeat(" ", MaxRecordBytes+1)), []byte(strings.Replace(string(data), `"stage":`, `"unknown":"not-output","stage":`, 1))} {
		if _, err := DecodeDiagnostic(bad); err == nil {
			t.Error("invalid_diagnostic_wire_accepted")
		}
	}
	for _, stage := range []string{StageGuard, StageDiscovery, StageCapture, StageCensus, StageImport, StageAccounts, StageStability, StageCleanup} {
		d := DiagnosticFor(owner, stage, []string{})
		wire, _ := Encode(d)
		if _, err := DecodeDiagnostic(wire); err != nil {
			t.Error("fixed_stage_refused")
		}
	}
}
