package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestCalendarObservationExecutableRequiresExplicitMode(t *testing.T) {
	supervisor, binary := os.Getenv("OWA_ACCEPTANCE_SUPERVISOR_BINARY"), os.Getenv("OWA_CALENDAR_ACCEPTANCE_BINARY")
	if supervisor == "" || binary == "" {
		t.Skip("explicit current synthetic supervisor/acceptance artifacts required")
	}
	for _, enabled := range []bool{false, true} {
		source := t.TempDir()
		copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-fixture", "calendar"), filepath.Join(source, "Default"))
		receipt := filepath.Join(t.TempDir(), "receipt")
		args := []string{"-test-binary", binary, "-test-sha256", testDigest(t, binary), "-outlook-root", source, "-scratch-parent", t.TempDir(), "-receipt-dir", receipt}
		if enabled {
			args = append(args, "-calendar-observation")
		}
		// #nosec G204 G702 -- Explicit locally built supervisor; all source/scratch/receipt arguments are owned synthetic fixtures, never a host cache.
		output, err := exec.CommandContext(t.Context(), supervisor, args...).CombinedOutput()
		if !enabled {
			if err == nil || string(output) != "owa_acceptance status=refused reason=arguments\n" {
				t.Fatal("old_front_door_mode_not_refused")
			}
			if _, statErr := os.Stat(receipt); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("missing_mode_created_receipt_or_launched")
			}
			continue
		}
		if err != nil || !strings.HasPrefix(string(output), "owa_calendar_observation status=observed cleanup=verified ") || strings.Contains(string(output), "qualified") {
			t.Fatal("calendar_front_door_claimed_wrong_scope_or_failed")
		}
	}
}

func TestCalendarCapableArtifactRefusesLegacyMailScopeBeforeDiscovery(t *testing.T) {
	binary := os.Getenv("OWA_CALENDAR_ACCEPTANCE_BINARY")
	if binary == "" {
		t.Skip("explicit current synthetic acceptance artifact required")
	}
	source := t.TempDir()
	copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-fixture", "calendar"), filepath.Join(source, "Default"))
	receipt := filepath.Join(t.TempDir(), "receipt")
	if _, err := runQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second); err == nil {
		t.Fatal("calendar_capable_artifact_accepted_old_mail_scope")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.DiagnosticName))
	if err != nil {
		t.Fatal("old_scope_refusal_diagnostic_missing")
	}
	diagnostic, err := protocol.DecodeDiagnostic(wire)
	if err != nil || diagnostic.Stage != protocol.StageGuard || diagnostic.Reason != "qualification_protocol_refused" {
		t.Fatal("old_scope_did_not_refuse_before_discovery")
	}
	wire, err = protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
	if err != nil {
		t.Fatal("old_scope_failed_closeout_missing")
	}
	closeout, err := protocol.DecodeCloseout(wire)
	if err != nil || closeout.Qualified || closeout.RootState != "absent" || !closeout.ConsumersReleased || !closeout.NativeCloseVerified {
		t.Fatal("old_scope_refusal_cleanup_unproved")
	}
}
