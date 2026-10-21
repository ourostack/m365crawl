package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalendarProductExecutableRequiresDistinctExclusiveMode(t *testing.T) {
	supervisor := os.Getenv("OWA_ACCEPTANCE_SUPERVISOR_BINARY")
	if supervisor == "" {
		t.Skip("explicit current synthetic supervisor artifact required")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_controller_test_binary_missing")
	}
	for _, mode := range []string{"none", "both", "qualification"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			args := []string{"-test-binary", binary, "-test-sha256", testDigest(t, binary),
				"-outlook-root", calendarProductSource(t, "qualified"), "-scratch-parent", t.TempDir(), "-receipt-dir", receipt}
			if mode != "none" {
				args = append(args, "-calendar-qualification")
			}
			if mode == "both" {
				args = append(args, "-calendar-observation")
			}
			// #nosec G204 G702 -- Explicit local supervisor plus this test binary and mandatory synthetic marker; no host cache.
			output, err := exec.CommandContext(t.Context(), supervisor, args...).CombinedOutput()
			if mode != "qualification" {
				if err == nil || string(output) != "owa_acceptance status=refused reason=arguments\n" {
					t.Fatal("ambiguous_or_missing_scope_mode_not_refused")
				}
				if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refused_mode_created_receipt")
				}
				return
			}
			if err != nil || !strings.HasPrefix(string(output), "owa_calendar_qualification status=qualified cleanup=verified ") {
				t.Fatal("explicit_product_mode_failed_or_claimed_observation")
			}
		})
	}
}
