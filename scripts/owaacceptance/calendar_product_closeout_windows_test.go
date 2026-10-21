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

func TestCalendarProductRefusesBinaryMismatchBeforeSideEffects(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	value, err := runCalendarQualified(exe, strings.Repeat("f", 64), calendarProductSource(t, "qualified"), t.TempDir(), receipt, time.Second)
	if err == nil || err.Error() != "qualification_binary_refused" || value.Outcome != "" {
		t.Fatal("product_binary_mismatch_accepted")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("product_binary_mismatch_created_receipt")
	}
}

func TestCalendarProductCloseoutPreservesTerminalFailures(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	for _, mode := range []string{"nonzero", "pin-close", "existing-record", "foreign-receipt", "invalid-owner", "oversize-owner"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			w, q, err := newBoundRun(exe, testDigest(t, exe), calendarProductSource(t, "qualified"), t.TempDir(), receipt, protocol.CalendarQualificationTest)
			if err != nil || w.cleanup() != nil {
				t.Fatal("synthetic_product_setup_or_never_started_cleanup_failed")
			}
			primary := errors.New("synthetic_primary_product_failure")
			exit, result := 1, error(primary)
			path := filepath.Join(receipt, protocol.CalendarQualificationCloseoutName)
			switch mode {
			case "nonzero":
				result = nil
			case "pin-close":
				q.pins = append(q.pins, windows.Handle(0))
			case "existing-record":
				if protocol.WriteProtectedNew(path, []byte("retained-synthetic-product-record")) != nil {
					t.Fatal("synthetic_prior_product_record_failed")
				}
			case "foreign-receipt":
				q.receiptNative[2]++
			case "invalid-owner":
				q.owner.Version++
			case "oversize-owner":
				q.owner.Source = strings.Repeat("synthetic-private", protocol.MaxRecordBytes)
			}
			finishErr := q.finish(exit, result)
			if finishErr == nil || result != nil && !errors.Is(finishErr, primary) || strings.Contains(finishErr.Error(), "synthetic-private") {
				t.Fatal("terminal_product_error_lost_or_private_value_disclosed")
			}
			if mode == "existing-record" || mode == "foreign-receipt" || mode == "oversize-owner" {
				if q.closeoutVerified || !strings.Contains(finishErr.Error(), "calendar_qualification_closeout_write_refused") {
					t.Fatal("unwritten_or_unowned_product_closeout_claimed_verified")
				}
				if mode == "existing-record" {
					data, readErr := protocol.ReadProtected(path)
					if readErr != nil || string(data) != "retained-synthetic-product-record" {
						t.Fatal("prior_product_closeout_overwritten")
					}
				} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refused_product_closeout_was_written")
				}
				return
			}
			if mode == "invalid-owner" {
				if q.closeoutVerified || !strings.Contains(finishErr.Error(), "calendar_qualification_closeout_refused") {
					t.Fatal("invalid_product_closeout_claimed_verified")
				}
				return
			}
			data, err := protocol.ReadProtected(path)
			if err != nil {
				t.Fatal("terminal_product_failure_record_missing")
			}
			record, err := protocol.DecodeCalendarQualificationCloseout(data)
			if err != nil || record.Qualified || record.RootState != "absent" || record.NativeCloseVerified || !q.closeoutVerified {
				t.Fatal("terminal_product_failure_claimed_release_or_qualification")
			}
			if mode == "nonzero" && (record.Outcome != "test_failed" || record.Reason != "test_exit_failed") {
				t.Fatal("product_nonzero_exit_fact_lost")
			}
			if mode == "pin-close" && (record.Outcome != "refused" || record.Reason != "binary_close_failed") {
				t.Fatal("product_pin_close_failure_lost")
			}
		})
	}
}
