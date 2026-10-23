package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestCalendarProductActualArtifactRefusesSeveralProfilesBeforeCapture(t *testing.T) {
	binary := os.Getenv("OWA_CALENDAR_ACCEPTANCE_BINARY")
	if binary == "" {
		t.Skip("explicit current compiled acceptance artifact required; synthetic sources only")
	}
	source := t.TempDir()
	fixture := filepath.Join("..", "..", "testdata", "outlook-owa-calendar-local-fixture", "calendar")
	for _, profile := range []string{"SyntheticA", "SyntheticB"} {
		copySyntheticFixture(t, fixture, filepath.Join(source, profile))
	}
	receipt := filepath.Join(t.TempDir(), "receipt")
	if _, err := runCalendarQualified(binary, testDigest(t, binary), source, t.TempDir(), receipt, 30*time.Second); err == nil {
		t.Fatal("several_profiles_exceeded_closed_qualification_scope")
	}
	wire, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CalendarQualificationFailureName))
	if err != nil {
		t.Fatal("profile_scope_refusal_record_missing")
	}
	refusal, err := protocol.DecodeCalendarQualificationFailure(wire)
	if err != nil || refusal.Stage != protocol.StageDiscovery || refusal.Reason != "profile_discovery_refused" {
		t.Fatal("several_profile_refusal_did_not_precede_capture")
	}
}
