package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestDiagnosticFailurePacketNeverPrintsCause(t *testing.T) {
	err := diagnosticFailure(protocol.StageCensus, []string{"ical_uid"}, errors.New("private_path_or_source_never_output"))
	if failurePacket(err) != `owa_acceptance status=unqualified stage=independent_census fields=["ical_uid"]` {
		t.Fatal("fixed_diagnostic_packet_missing")
	}
	for _, err := range []error{errors.New("private"), diagnosticFailure("private", []string{"subject"}, errors.New("private")), diagnosticFailure(protocol.StageImport, []string{"private"}, errors.New("private")), errTimeout} {
		if failurePacket(err) != "owa_acceptance status=unqualified stage=unproved fields=[]" {
			t.Fatal("untrusted_failure_packet_accepted")
		}
	}
}

func TestDiagnosticWriteRefusalAndTimeoutRemainUnproved(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"write_refused", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			source := t.TempDir()
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "multiple"), filepath.Join(source, "Default"))
			w, q, err := newQualifiedRun(binary, testDigest(t, binary), source, t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
			if err != nil {
				t.Fatal("synthetic_workspace_failed")
			}
			if mode == "write_refused" && protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.DiagnosticName), []byte("{}")) != nil {
				t.Fatal("synthetic_write_block_failed")
			}
			timeout := 30 * time.Second
			if mode == "timeout" {
				timeout = time.Millisecond
			}
			exit, result := w.run(binary, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), timeout)
			finishErr := q.finish(exit, result)
			if finishErr == nil || q.diagnostic.Stage != "" || failurePacket(finishErr) != "owa_acceptance status=unqualified stage=unproved fields=[]" {
				t.Fatal("failed_diagnostic_or_timeout_became_proof")
			}
			data, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CloseoutName))
			if err != nil {
				t.Fatal("failure_closeout_missing")
			}
			record, err := protocol.DecodeCloseout(data)
			if err != nil || record.Qualified || record.RootState != "absent" || !record.ConsumersReleased || !record.NativeCloseVerified {
				t.Fatal("fallback_failure_closeout_unproved")
			}
		})
	}
}

func TestDiagnosticAloneCannotQualifyZeroExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_failed")
	}
	w, q, err := newQualifiedRun(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
	if err != nil {
		t.Fatal("synthetic_workspace_failed")
	}
	if protocol.WriteProtectedNew(filepath.Join(q.receipt, protocol.DiagnosticName), []byte("{}")) != nil {
		t.Fatal("private_failure_record_failed")
	}
	exit, result := w.run(exe, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), 5*time.Second)
	if q.finish(exit, result) == nil || q.witness.Outcome == "passed" {
		t.Fatal("diagnostic_record_qualified")
	}
}
func TestDiagnosticActualCompiledSourceRefusal(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	source := t.TempDir()
	copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "multiple"), filepath.Join(source, "Default"))
	receipt := filepath.Join(t.TempDir(), "receipt")
	_, err := runQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second)
	if err == nil || failurePacket(err) != `owa_acceptance status=unqualified stage=independent_census fields=[]` {
		t.Fatal("actual_failed_test_diagnostic_missing")
	}
	data, readErr := protocol.ReadProtected(filepath.Join(receipt, protocol.DiagnosticName))
	if readErr != nil {
		t.Fatal("private_failure_witness_missing")
	}
	d, err := protocol.DecodeDiagnostic(data)
	if err != nil || d.Stage != protocol.StageCensus || d.Outcome != "failed" {
		t.Fatal("actual_failure_stage_unproved")
	}
	if _, err := os.Lstat(filepath.Join(receipt, protocol.WitnessName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed_test_produced_qualification")
	}
}

func TestDiagnosticConsumerRejectsMissingUnknownAndForeign(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_artifact_failed")
	}
	for _, mode := range []string{"missing", "unknown", "foreign", "trailing", "extra", "oversized", "negative_counts", "valid"} {
		t.Run(mode, func(t *testing.T) {
			w, q, err := newQualifiedRun(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
			if err != nil {
				t.Fatal("synthetic_workspace_failed")
			}
			// Matching adopted identity is only for bounded consumer wire validation, not a process-run or qualification claim.
			q.owner.Adopted = true
			q.owner.Child = protocol.Process{PID: q.owner.Supervisor.PID + 1, Created: 1}
			d := protocol.DiagnosticFor(q.owner, protocol.StageImport, []string{"subject"})
			switch mode {
			case "unknown":
				d.Stage = "private"
			case "foreign":
				d.Nonce = strings.Repeat("f", 64)
			}
			data, _ := protocol.Encode(d)
			switch mode {
			case "trailing":
				data = append(data, []byte("{}")...)
			case "extra":
				data = append(data[:len(data)-1], []byte(`,"private":"never-output"}`)...)
			case "oversized":
				data = make([]byte, protocol.MaxRecordBytes+1)
			case "negative_counts":
				data = append(data[:len(data)-1], []byte(`,"counts":{"messages":-1}}`)...)
			}
			path := filepath.Join(q.receipt, protocol.DiagnosticName)
			if mode != "missing" {
				if mode == "oversized" {
					if protocol.WriteProtectedNew(path, []byte("{}")) != nil {
						t.Fatal("private_record_failed")
					}
					if os.WriteFile(path, data, 0o600) != nil {
						t.Fatal("synthetic_oversize_write_failed")
					}
				} else if protocol.WriteProtectedNew(path, data) != nil {
					t.Fatal("synthetic_record_failed")
				}
			}
			q.readDiagnostic()
			if mode == "valid" {
				if q.diagnostic.Stage != protocol.StageImport {
					t.Error("valid_failed_run_stage_missing")
				}
			} else if q.diagnostic.Stage != "" {
				t.Error("untrusted_diagnostic_consumed")
			}
			if w.cleanup() != nil {
				t.Fatal("synthetic_workspace_cleanup_failed")
			}
			if errors.Join(q.binary.Close(), q.closePins()) != nil {
				t.Fatal("synthetic_native_close_failed")
			}
		})
	}
}
