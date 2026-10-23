package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestCalendarProductActualCopiedSyntheticArtifact(t *testing.T) {
	binary := os.Getenv("OWA_CALENDAR_ACCEPTANCE_BINARY")
	if binary == "" {
		t.Skip("explicit current compiled acceptance artifact required; synthetic sources only")
	}
	for _, tc := range []struct {
		family, variant string
		qualified       bool
		stage           string
	}{
		{"outlook-owa-calendar-local-fixture", "calendar", true, ""},
		{"outlook-owa-calendar-local-fixture", "calendar-empty", false, protocol.StageCalendarOracle},
		{"outlook-owa-calendar-local-fixture", "calendar-damaged", false, protocol.StageCalendarOracle},
		{"outlook-owa-calendar-fixture", "calendar", false, protocol.StageImport},
		{"outlook-owa-fixture", "multiple", false, protocol.StageCalendarOracle},
	} {
		t.Run(tc.family+"/"+tc.variant, func(t *testing.T) {
			source := t.TempDir()
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", tc.family, tc.variant), filepath.Join(source, "Default"))
			receipt := filepath.Join(t.TempDir(), "receipt")
			value, runErr := runCalendarQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second)
			if (runErr == nil) != tc.qualified {
				t.Fatal("actual_copied_synthetic_product_verdict_mismatch")
			}
			wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationCloseoutName))
			if err != nil {
				t.Fatal("actual_product_terminal_closeout_missing")
			}
			closed, err := protocol.DecodeCalendarQualificationCloseout(wire)
			if err != nil || closed.Qualified != tc.qualified || closed.RootState != "absent" ||
				!closed.ConsumersReleased || !closed.NativeCloseVerified {
				t.Fatal("actual_product_release_or_scope_proof_failed")
			}
			if tc.qualified {
				if value.ExpectedEvents != 2 || value.PersistedEvents != 2 || value.ComparedValues != 288 || value.Counts["records"] != 2 {
					t.Fatal("actual_product_raw_sql_or_repeat_volume_mismatch")
				}
			} else {
				wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationFailureName))
				if err != nil {
					t.Fatal("actual_product_failure_diagnostic_missing")
				}
				refusal, err := protocol.DecodeCalendarQualificationFailure(wire)
				if err != nil || refusal.Stage != tc.stage || protocol.MatchCalendarQualificationFailure(closed.Owner, refusal) != nil {
					t.Fatal("actual_product_failure_stage_or_identity_mismatch")
				}
			}
		})
	}
}
