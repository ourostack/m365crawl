//go:build acceptance && windows

package acceptance

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func owaCalendarProductCleanup(t *testing.T, owner protocol.Ownership, state *owaFailureState, witness *protocol.CalendarQualification) {
	t.Cleanup(func() {
		if !t.Failed() {
			if !state.completed || protocol.EmitCalendarQualification(owner, *witness) != nil {
				t.Error("calendar_product_witness_refused")
			}
			return
		}
		stage := state.failure
		if stage == "" {
			stage = state.stage
			if state.completed {
				stage = protocol.StageCleanup
			}
		}
		reason := state.reason
		if reason == "" {
			reason = "unproved"
		}
		fields, omissions := state.fields, state.omissions
		if fields == nil {
			fields = []string{}
		}
		if omissions == nil {
			omissions = map[string]int64{}
		}
		value := protocol.CalendarQualificationFailure{
			Version: protocol.Version, Kind: protocol.CalendarQualificationFailureKind, Owner: owner,
			Stage: stage, Reason: reason, Fields: fields, Omissions: omissions,
		}
		if protocol.EmitCalendarQualificationFailure(owner, value) != nil {
			t.Error("calendar_product_failure_record_refused")
		}
	})
}

func TestRealOWACalendarProductQualification(t *testing.T) {
	requireReal(t)
	owner, err := protocol.CurrentCalendarQualificationGuard()
	if err != nil {
		t.Fatal("calendar_product_guard_refused")
	}
	state := &owaFailureState{stage: protocol.StageDiscovery, calendar: true}
	witness := protocol.CalendarQualification{
		Version: protocol.Version, Kind: protocol.CalendarQualificationKind, Nonce: owner.Nonce, Test: owner.Test,
		BinaryHash: owner.BinaryHash, BinaryNative: owner.BinaryNative, RootNative: owner.RootNative,
		Supervisor: owner.Supervisor, Child: owner.Child, Outcome: "qualified", Observed: 1,
	}
	owaCalendarProductCleanup(t, owner, state, &witness)
	privateDiagnostic := owaPrivateDiagnostic(t, owner)
	profiles, skipped, err := outlookowa.Discover(owner.Source)
	if err != nil || len(skipped) != 0 || len(profiles) != 1 {
		state.record(protocol.StageDiscovery, "profile_discovery_refused")
		t.Fatal("calendar_product_profile_scope_refused")
	}
	captured := owaCapture(t, profiles[0], state)
	if !captured.stable {
		state.record(protocol.StageStability, "unstable_source_observation")
		t.Fatal("calendar_product_snapshot_unstable")
	}
	privateDiagnostic.read(t, captured, state)
	state.stage = protocol.StageCalendarOracle
	*captured.readersReleased = false
	expected, err := owaReadExpectedCalendar(captured.ctx, captured.profile.Source.LevelDBDir, captured.profile.Source.BlobDir)
	*captured.readersReleased = expected.Released
	if err != nil || !expected.Released || len(expected.Rows) == 0 || len(owaCalendarSQLColumns) != protocol.CalendarSQLFieldCount {
		reason := "calendar_source_volume_refused"
		if err != nil {
			reason = protocol.CalendarQualificationOracleReason(err)
		}
		state.record(protocol.StageCalendarOracle, reason)
		t.Fatal("calendar_product_expected_read_refused")
	}
	state.stage = protocol.StageCensus
	mailReleased := false
	*captured.readersReleased = false
	captured.census, err = owaCensusLeaseContext(captured.ctx, captured.profile.Source.LevelDBDir, captured.profile.Source.BlobDir, &mailReleased)
	*captured.readersReleased = expected.Released && mailReleased
	if err != nil || !mailReleased {
		state.record(protocol.StageCensus, protocol.CensusReason(err))
		t.Fatal("calendar_product_independent_mail_control_refused")
	}
	owaCapturedIntegrity(t, &captured)
	witness.Counts, witness.ExpectedEvents = expected.Counts, int64(len(expected.Rows))
	db := filepath.Join(owaScratch(t, state), "calendar-product.db")
	opts := syncer.Options{Root: filepath.Join(captured.root, "no-teams"), DBPath: db, OutlookRoot: captured.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	var baseline map[string][][]any
	var history [][]any
	for step := range protocol.CalendarQualificationPasses {
		state.stage = protocol.StageImport
		opts.FullRead = step == 2
		rep, _, runErr := syncer.Run(captured.ctx, opts)
		owaSyncChecks(t, rep, runErr, captured, step)
		st := owaArchive(t, db, state)
		owaArchiveFindings(t, state, owaCompareContext(captured.ctx, st, captured.account, captured.census, state))
		current := owaArchiveState(captured.ctx, t, st, state)
		if step == 0 {
			baseline = current
		} else {
			owaRepeatState(t, state, current, baseline)
		}
		owaGlobalAccounts(t, st, []owaObservation{captured}, state)
		state.stage = protocol.StageCalendarSQL
		findings, compareErr := owaCompareCalendarContext(captured.ctx, st, captured.account, expected)
		if compareErr != nil || len(findings) != 0 {
			reason := "archive_mismatch"
			if compareErr != nil {
				reason = protocol.CalendarQualificationOracleReason(compareErr)
			}
			state.recordFields(protocol.StageCalendarSQL, reason, findings)
			t.Fatal("calendar_product_sql_comparison_refused")
		}
		rows, persisted, queryErr := owaCalendarProductHistory(captured.ctx, st, captured.account)
		if queryErr != nil {
			if errors.Is(queryErr, errOWACalendarAccountScope) {
				state.recordFields(protocol.StageCalendarSQL, "archive_mismatch", []string{"source", "account_id"})
			} else {
				state.record(protocol.StageCalendarHistory, "archive_state_refused")
			}
			t.Fatal("calendar_product_history_projection_refused")
		}
		if step == 0 {
			history = rows
		} else if !reflect.DeepEqual(rows, history) {
			state.record(protocol.StageCalendarHistory, "repeat_archive_unstable")
			t.Fatal("calendar_product_semantic_history_changed")
		}
		state.cleanup(t, func() { owaCloseArchive(t, st, state) })
		if t.Failed() {
			return
		}
		witness.PersistedEvents = persisted
		witness.Passes++
		witness.ComparedValues += witness.ExpectedEvents * int64(len(owaCalendarSQLColumns))
	}
	owaCapturedIntegrity(t, &captured)
	witness.MailControlPassed, witness.ValueReadersReleased = true, *captured.readersReleased
	state.completed = true
}
