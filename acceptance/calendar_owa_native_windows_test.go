//go:build acceptance && windows

package acceptance

import (
	"errors"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestRealOWACalendarFieldObservation(t *testing.T) {
	requireReal(t)
	owner, err := protocol.CurrentCalendarObservationGuard()
	if err != nil {
		t.Fatal("calendar_observation_guard_refused")
	}
	counts := map[string]int64{}
	for _, field := range protocol.CalendarObservationFields() {
		counts[field] = 0
	}
	released := true
	t.Cleanup(func() {
		if !t.Failed() && protocol.EmitCalendarObservation(owner, counts, 1, 0, released) != nil {
			t.Error("calendar_observation_emit_refused")
		}
	})
	profiles, skipped, err := outlookowa.Discover(owner.Source)
	if err != nil || len(skipped) != 0 || len(profiles) != 1 {
		t.Fatal("calendar_observation_profile_scope_refused")
	}
	captured := owaCapture(t, profiles[0])
	if !captured.stable {
		t.Fatal("calendar_observation_source_unstable")
	}
	*captured.readersReleased = false
	survey, err := owaSurveyCalendar(captured.ctx, captured.profile.Source.LevelDBDir, captured.profile.Source.BlobDir)
	*captured.readersReleased, released = survey.Released, survey.Released
	if err != nil || !survey.Released {
		t.Fatal("calendar_observation_survey_refused")
	}
	counts = survey.Counts
	owaCapturedIntegrity(t, &captured)
}

func TestOWACalendarCleanupRequiresReaderRelease(t *testing.T) {
	released, calls := false, 0
	cleanup := owaReleaseGatedCleanup(&released, func() { calls++ })
	cleanup()
	if calls != 0 {
		t.Fatal("unproved_reader_payload_removed")
	}
	released = true
	cleanup()
	if calls != 1 {
		t.Fatal("proved_reader_payload_not_released")
	}
	owaReleaseGatedCleanup(nil, func() { calls++ })()
	if calls != 1 {
		t.Fatal("unknown_release_treated_as_proved")
	}
}

func TestOWACalendarFeatureRefusesMailOnlyPermit(t *testing.T) {
	if err := owaRealAcceptancePermit(); err == nil || err.Error() != "mail_only_calendar_scope_refused" {
		t.Fatal("calendar_capable_feature_did_not_explicitly_refuse_old_mail_scope")
	}
}

func TestOWACalendarRegistryCleanupWaitsForActualReaderRelease(t *testing.T) {
	captured := owaCapture(t, owaFixtureProfile("mail", "Synthetic"))
	*captured.readersReleased = false
	calMu.Lock()
	cleanup := calCleanups[len(calCleanups)-1]
	calMu.Unlock()
	cleanup()
	if _, err := os.Stat(captured.ownedSnapshot); err != nil {
		t.Fatal("registry_removed_unproved_reader_snapshot")
	}
	survey, err := owaSurveyCalendar(captured.ctx, captured.profile.Source.LevelDBDir, captured.profile.Source.BlobDir)
	if err != nil || !survey.Released {
		t.Fatal("synthetic_actual_reader_release_unproved")
	}
	*captured.readersReleased = survey.Released
	cleanup()
	if _, err := os.Stat(captured.ownedSnapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("proved_registry_snapshot_not_removed")
	}
}
