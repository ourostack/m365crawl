package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func observationSource(t *testing.T, mode string) string {
	t.Helper()
	source := t.TempDir()
	if os.WriteFile(filepath.Join(source, "synthetic-calendar-control"), []byte(mode), 0o600) != nil {
		t.Fatal("synthetic_observation_source_failed")
	}
	return source
}

func TestCalendarObservedActualSyntheticArtifact(t *testing.T) {
	binary := os.Getenv("OWA_CALENDAR_ACCEPTANCE_BINARY")
	if binary == "" {
		t.Skip("explicit current copied-synthetic acceptance artifact required")
	}
	for _, tc := range []struct {
		fixture string
		variant string
		records int64
		failed  int64
	}{
		{"outlook-owa-calendar-fixture", "calendar", 2, 0},
		{"outlook-owa-calendar-empty-mail-fixture", "calendar-empty-mail", 0, 0},
		{"outlook-owa-calendar-fixture", "calendar-damaged", 3, 1},
		{"outlook-owa-fixture", "mail", 1, 1},
	} {
		t.Run(tc.variant, func(t *testing.T) {
			source := t.TempDir()
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", tc.fixture, tc.variant), filepath.Join(source, "Default"))
			receipt := filepath.Join(t.TempDir(), "receipt")
			value, err := runCalendarObserved(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second)
			if err != nil || value.Observed != 1 || value.Counts["records"] != tc.records || value.Counts["decode_failed"] != tc.failed {
				t.Fatal("actual_synthetic_calendar_observation_missing")
			}
			wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarObservationCloseoutName))
			if err != nil {
				t.Fatal("actual_observation_closeout_missing")
			}
			record, err := protocol.DecodeCalendarObservationCloseout(wire)
			if err != nil || !record.ObservationConfirmed || record.RootState != "absent" || !record.NativeCloseVerified {
				t.Fatal("actual_observation_cleanup_unproved")
			}
			if _, err := os.Stat(filepath.Join(receipt, protocol.WitnessName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("shape_observation_emitted_qualification")
			}
		})
	}
}

// Only this package's synthetic binary uses the marker; it never surveys a host cache.
func TestRealOWACalendarFieldObservation(t *testing.T) {
	if os.Getenv("M365CRAWL_REAL_CACHE") != "1" {
		t.Skip("supervised synthetic subprocess only")
	}
	owner, err := protocol.CurrentCalendarObservationGuard()
	if err != nil {
		t.Fatal("synthetic_observation_guard_refused")
	}
	// #nosec G304 -- Fixed synthetic control marker; a missing marker refuses before any source values.
	mode, err := os.ReadFile(filepath.Join(owner.Source, "synthetic-calendar-control"))
	if err != nil {
		t.Fatal("synthetic_observation_marker_required")
	}
	switch string(mode) {
	case "missing":
		return
	case "failed":
		t.Fatal("synthetic_observation_failed")
	case "sleep":
		time.Sleep(time.Minute)
		return
	case "observed":
	case "forged", "bad-counters", "unreleased", "mail-witness":
		counts := map[string]int64{}
		for _, field := range protocol.CalendarObservationFields() {
			counts[field] = 0
		}
		value := protocol.CalendarObservation{
			Version: protocol.Version, Kind: protocol.CalendarObservationKind, Nonce: owner.Nonce, Test: owner.Test,
			BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative,
			Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "observed", Observed: 1,
			ValueReadersReleased: true, Counts: counts,
		}
		switch string(mode) {
		case "forged":
			value.Child.Created++
		case "bad-counters":
			value.Counts["records"] = 1
		case "unreleased":
			value.ValueReadersReleased = false
		}
		name := protocol.CalendarObservationName
		if string(mode) == "mail-witness" {
			name = protocol.WitnessName
		}
		wire, err := protocol.Encode(value)
		if err != nil || protocol.WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(protocol.OwnerEnv)), name), wire) != nil {
			t.Fatal("synthetic_forged_observation_write_failed")
		}
		return
	default:
		t.Fatal("synthetic_observation_mode_refused")
	}
	counts := map[string]int64{}
	for _, field := range protocol.CalendarObservationFields() {
		counts[field] = 0
	}
	if protocol.EmitCalendarObservation(owner, counts, 1, 0, true) != nil {
		t.Fatal("synthetic_observation_emit_failed")
	}
}

func TestCalendarObservedSupervisorUsesDistinctWitnessAndCloseout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	value, err := runCalendarObserved(exe, testDigest(t, exe), observationSource(t, "observed"), t.TempDir(), receipt, 10*time.Second)
	if err != nil || value.Outcome != "observed" || value.Observed != 1 || value.Counts["records"] != 0 {
		t.Fatal("synthetic_calendar_observation_missing")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarObservationCloseoutName))
	if err != nil {
		t.Fatal("observation_closeout_missing")
	}
	closed, err := protocol.DecodeCalendarObservationCloseout(wire)
	if err != nil || !closed.ObservationConfirmed || closed.RootState != "absent" || !closed.ConsumersReleased || !closed.NativeCloseVerified {
		t.Fatal("observation_lifecycle_unproved")
	}
	if _, err := protocol.DecodeCloseout(wire); err == nil {
		t.Fatal("observation_claimed_qualification")
	}
}

func TestCalendarObservedSupervisorRefusesMissingFailureAndTimeout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	for _, mode := range []string{"missing", "failed", "sleep", "forged", "bad-counters", "unreleased", "mail-witness"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			budget := 10 * time.Second
			if mode == "sleep" {
				budget = time.Millisecond
			}
			_, err := runCalendarObserved(exe, testDigest(t, exe), observationSource(t, mode), t.TempDir(), receipt, budget)
			if err == nil {
				t.Fatal("failed_or_missing_observation_accepted")
			}
			if mode == "sleep" && !errors.Is(err, errTimeout) {
				t.Fatal("observation_timeout_lost")
			}
			wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarObservationCloseoutName))
			if err != nil {
				t.Fatal("failed_observation_closeout_missing")
			}
			closed, err := protocol.DecodeCalendarObservationCloseout(wire)
			if err != nil || closed.ObservationConfirmed || closed.RootState != "absent" || !closed.ConsumersReleased || !closed.NativeCloseVerified {
				t.Fatal("failed_observation_release_unproved")
			}
		})
	}
}

func TestCalendarObservedSupervisorRetainsUnprovedGeneration(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	w, q, err := newBoundRun(exe, testDigest(t, exe), observationSource(t, "observed"), t.TempDir(), receipt, protocol.CalendarObservationTest)
	if err != nil {
		t.Fatal("synthetic_observation_setup_failed")
	}
	t.Cleanup(func() {
		// No child was ever started; disposal is safe only after the retained-facts assertion.
		if err := w.cleanup(); err != nil {
			t.Error("synthetic_retained_root_cleanup_failed")
		}
	})
	if q.finish(1, errors.New("synthetic-unproved-generation")) == nil {
		t.Fatal("unproved_observation_generation_confirmed")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarObservationCloseoutName))
	if err != nil {
		t.Fatal("retained_observation_closeout_missing")
	}
	record, err := protocol.DecodeCalendarObservationCloseout(wire)
	if err != nil || record.ObservationConfirmed || record.RootState != "retained_owned" || record.ConsumersReleased ||
		record.ObservedRootNative != q.owner.RootNative || record.Owner.Adopted {
		t.Fatal("unproved_observation_recovery_facts_lost")
	}
	if _, err := os.Stat(w.dir); err != nil {
		t.Fatal("unproved_generation_payload_removed")
	}
}
