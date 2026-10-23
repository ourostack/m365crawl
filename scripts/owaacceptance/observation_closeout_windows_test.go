package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
	"golang.org/x/sys/windows"
)

func TestCalendarObservedRefusesUnboundScopeBeforeSideEffects(t *testing.T) {
	receipt := filepath.Join(t.TempDir(), "receipt")
	w, q, err := newBoundRun("", "", "", "", receipt, "unbound-calendar-test")
	if err == nil || err.Error() != "qualification_scope_refused" || w != nil || q != nil {
		t.Fatal("unbound_scope_not_refused")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unbound_scope_created_receipt")
	}
}

func TestCalendarObservedRefusesBinaryMismatchBeforeLaunch(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	value, err := runCalendarObserved(exe, strings.Repeat("f", 64), observationSource(t, "observed"), t.TempDir(), receipt, time.Second)
	if err == nil || err.Error() != "qualification_binary_refused" || value.Outcome != "" {
		t.Fatal("calendar_binary_mismatch_accepted")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("binary_mismatch_created_receipt")
	}
}

func TestCalendarObservationCloseoutPreservesTerminalFailures(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	for _, mode := range []string{"nonzero", "pin-close", "existing-record", "foreign-receipt", "invalid-owner"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			w, q, err := newBoundRun(exe, testDigest(t, exe), observationSource(t, "observed"), t.TempDir(), receipt, protocol.CalendarObservationTest)
			if err != nil {
				t.Fatal("synthetic_observation_setup_failed")
			}
			if err := w.cleanup(); err != nil {
				t.Fatal("never_started_workspace_cleanup_failed")
			}
			primary := errors.New("synthetic_primary_lifecycle_failure")
			exit, result := 1, error(primary)
			path := filepath.Join(receipt, protocol.CalendarObservationCloseoutName)
			switch mode {
			case "nonzero":
				result = nil
			case "pin-close":
				q.pins = append(q.pins, windows.Handle(0))
			case "existing-record":
				if protocol.WriteProtectedNew(path, []byte("retained-synthetic-record")) != nil {
					t.Fatal("synthetic_prior_record_failed")
				}
			case "foreign-receipt":
				q.receiptNative[2]++
			case "invalid-owner":
				q.owner.Version++
			}
			finishErr := q.finish(exit, result)
			if finishErr == nil || result != nil && !errors.Is(finishErr, primary) {
				t.Fatal("terminal_failure_or_primary_error_lost")
			}
			if mode == "existing-record" || mode == "foreign-receipt" {
				if q.closeoutVerified || !strings.Contains(finishErr.Error(), "calendar_observation_closeout_write_refused") {
					t.Fatal("unowned_closeout_claimed_verified")
				}
				if mode == "existing-record" {
					data, readErr := protocol.ReadProtected(path)
					if readErr != nil || string(data) != "retained-synthetic-record" {
						t.Fatal("prior_closeout_overwritten")
					}
				} else if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("foreign_receipt_received_closeout")
				}
				return
			}
			if mode == "invalid-owner" {
				if q.closeoutVerified || !strings.Contains(finishErr.Error(), "calendar_observation_closeout_refused") {
					t.Fatal("invalid_closeout_claimed_verified")
				}
				return
			}
			data, err := protocol.ReadProtected(path)
			if err != nil {
				t.Fatal("terminal_failure_record_missing")
			}
			record, err := protocol.DecodeCalendarObservationCloseout(data)
			if err != nil || record.ObservationConfirmed || record.RootState != "absent" || record.NativeCloseVerified || !q.closeoutVerified {
				t.Fatal("terminal_failure_claimed_observation_or_close_proof")
			}
			if mode == "nonzero" && (record.Outcome != "test_failed" || record.Reason != "test_exit_failed") {
				t.Fatal("nonzero_exit_not_preserved")
			}
			if mode == "pin-close" && (record.Outcome != "refused" || record.Reason != "binary_close_failed") {
				t.Fatal("pin_close_failure_not_preserved")
			}
		})
	}
}
