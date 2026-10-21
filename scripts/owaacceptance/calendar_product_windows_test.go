package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func calendarProductSource(t *testing.T, mode string) string {
	t.Helper()
	source := t.TempDir()
	if os.WriteFile(filepath.Join(source, "synthetic-calendar-product-control"), []byte(mode), 0o600) != nil {
		t.Fatal("synthetic_product_control_setup_failed")
	}
	return source
}

// This controller test binary requires an owned marker and never reads an M365 cache.
func TestRealOWACalendarProductQualification(t *testing.T) {
	if os.Getenv("M365CRAWL_REAL_CACHE") != "1" {
		t.Skip("supervised synthetic subprocess only")
	}
	owner, err := protocol.CurrentCalendarQualificationGuard()
	if err != nil {
		t.Fatal("synthetic_product_guard_refused")
	}
	// #nosec G304 -- Mandatory fixed synthetic control marker; missing marker refuses without cache discovery.
	mode, err := os.ReadFile(filepath.Join(owner.Source, "synthetic-calendar-product-control"))
	if err != nil {
		t.Fatal("synthetic_product_marker_required")
	}
	switch string(mode) {
	case "diagnostic", "forged-diagnostic", "sql-diagnostic", "mail-diagnostic":
		failure := protocol.CalendarQualificationFailure{
			Version: protocol.Version, Kind: protocol.CalendarQualificationFailureKind, Owner: owner,
			Stage: protocol.StageCalendarOracle, Reason: "calendar_oracle_regional_zone_unsupported",
			Fields: []string{}, Omissions: map[string]int64{},
		}
		switch string(mode) {
		case "sql-diagnostic":
			failure.Stage, failure.Reason, failure.Fields = protocol.StageCalendarSQL, "archive_mismatch", []string{"is_private"}
		case "mail-diagnostic":
			failure.Stage, failure.Reason, failure.Fields = protocol.StageImport, "sync_source_refused", []string{"mail_coverage"}
			failure.Omissions = map[string]int64{"outlook_owa_reader_blob_missing": 2, "outlook_owa_source_changed": 1}
		}
		if string(mode) != "forged-diagnostic" {
			if protocol.EmitCalendarQualificationFailure(owner, failure) != nil {
				t.Fatal("synthetic_product_failure_emission_refused")
			}
		} else {
			failure.Owner.Child.Created++
			wire, err := protocol.Encode(failure)
			if err != nil || protocol.WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(protocol.OwnerEnv)), protocol.CalendarQualificationFailureName), wire) != nil {
				t.Fatal("synthetic_foreign_failure_setup_failed")
			}
		}
		t.Fatal("synthetic-private-child-error-discarded")
	case "missing":
		return
	case "failed":
		t.Fatal("synthetic_product_failed")
	case "sleep":
		time.Sleep(time.Minute)
		return
	case "qualified", "forged", "bad-counts", "unreleased", "mail-witness", "observation-witness":
	default:
		t.Fatal("synthetic_product_mode_refused")
	}
	counts := map[string]int64{}
	for _, field := range protocol.CalendarObservationFields() {
		counts[field] = 0
	}
	for _, field := range []string{"records", "decoded", "objects", "uid_valid", "start_valid", "end_valid", "modified_valid", "all_day_false", "body_unknown", "attendees_unknown"} {
		counts[field] = 2
	}
	value := protocol.CalendarQualification{
		Version: protocol.Version, Kind: protocol.CalendarQualificationKind, Nonce: owner.Nonce, Test: owner.Test,
		BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative,
		Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "qualified", Observed: 1,
		ValueReadersReleased: true, MailControlPassed: true, Counts: counts,
		ExpectedEvents: 2, PersistedEvents: 2, Passes: 3, ComparedValues: 288,
	}
	if string(mode) == "qualified" {
		if protocol.EmitCalendarQualification(owner, value) != nil {
			t.Fatal("synthetic_product_emission_refused")
		}
		return
	}
	name := protocol.CalendarQualificationName
	switch string(mode) {
	case "forged":
		value.Child.Created++
	case "bad-counts":
		value.ComparedValues--
	case "unreleased":
		value.ValueReadersReleased = false
	case "mail-witness":
		name = protocol.WitnessName
	case "observation-witness":
		name = protocol.CalendarObservationName
	}
	wire, err := protocol.Encode(value)
	if err != nil || protocol.WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(protocol.OwnerEnv)), name), wire) != nil {
		t.Fatal("synthetic_invalid_product_witness_setup_failed")
	}
}

func TestCalendarProductSupervisorBindsWitnessAndTerminalRelease(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	value, err := runCalendarQualified(exe, testDigest(t, exe), calendarProductSource(t, "qualified"), t.TempDir(), receipt, 10*time.Second)
	if err != nil || value.Outcome != "qualified" || value.ExpectedEvents != 2 || value.ComparedValues != 288 {
		t.Fatal("guarded_synthetic_product_qualification_failed")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationCloseoutName))
	if err != nil {
		t.Fatal("product_terminal_closeout_missing")
	}
	closed, err := protocol.DecodeCalendarQualificationCloseout(wire)
	if err != nil || !closed.Qualified || closed.RootState != "absent" || !closed.ConsumersReleased || !closed.NativeCloseVerified {
		t.Fatal("product_generation_release_or_root_absence_unproved")
	}
	for _, old := range []string{protocol.WitnessName, protocol.CloseoutName, protocol.CalendarObservationName, protocol.CalendarObservationCloseoutName} {
		if _, err := os.Stat(filepath.Join(receipt, old)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("product_route_wrote_old_scope_record")
		}
	}
}

func TestCalendarProductSupervisorRefusesPartialForgedAndFailedRuns(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	for _, mode := range []string{"missing", "failed", "sleep", "forged", "bad-counts", "unreleased", "mail-witness", "observation-witness"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			budget := 10 * time.Second
			if mode == "sleep" {
				budget = time.Millisecond
			}
			_, runErr := runCalendarQualified(exe, testDigest(t, exe), calendarProductSource(t, mode), t.TempDir(), receipt, budget)
			if runErr == nil || mode == "sleep" && !errors.Is(runErr, errTimeout) {
				t.Fatal("unproved_product_run_accepted_or_timeout_lost")
			}
			wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationCloseoutName))
			if err != nil {
				t.Fatal("failed_product_terminal_closeout_missing")
			}
			closed, err := protocol.DecodeCalendarQualificationCloseout(wire)
			if err != nil || closed.Qualified || closed.RootState != "absent" || !closed.ConsumersReleased || !closed.NativeCloseVerified {
				t.Fatal("failed_product_release_facts_lost_or_qualified")
			}
		})
	}
}

func TestCalendarProductSupervisorRetainsUnprovedGeneration(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	w, q, err := newBoundRun(exe, testDigest(t, exe), calendarProductSource(t, "qualified"), t.TempDir(), receipt, protocol.CalendarQualificationTest)
	if err != nil {
		t.Fatal("synthetic_product_setup_failed")
	}
	t.Cleanup(func() {
		if err := w.cleanup(); err != nil {
			t.Error("never_started_synthetic_root_cleanup_failed")
		}
	})
	if q.finish(1, errors.New("synthetic-unproved-generation")) == nil {
		t.Fatal("unproved_product_generation_qualified")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationCloseoutName))
	if err != nil {
		t.Fatal("retained_product_closeout_missing")
	}
	record, err := protocol.DecodeCalendarQualificationCloseout(wire)
	if err != nil || record.Qualified || record.RootState != "retained_owned" || record.ConsumersReleased ||
		record.ObservedRootNative != q.owner.RootNative || record.Owner.Adopted {
		t.Fatal("unproved_product_recovery_facts_lost")
	}
	if _, err := os.Stat(w.dir); err != nil {
		t.Fatal("unproved_product_generation_payload_removed")
	}
}
