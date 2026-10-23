package protocol

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func calendarFailure() CalendarQualificationFailure {
	return CalendarQualificationFailure{
		Version: Version, Kind: CalendarQualificationFailureKind, Owner: calendarQualificationOwner(),
		Stage: StageCalendarSQL, Reason: "archive_mismatch", Fields: []string{"is_private", "unknown_fields"}, Omissions: map[string]int64{},
	}
}

func TestCalendarQualificationFailureDistinctClosedScope(t *testing.T) {
	value := calendarFailure()
	wire, _ := Encode(value)
	decoded, err := DecodeCalendarQualificationFailure(wire)
	if err != nil || MatchCalendarQualificationFailure(value.Owner, decoded) != nil {
		t.Fatal("identity_bound_calendar_failure_refused")
	}
	if _, err := DecodeDiagnostic(wire); err == nil {
		t.Fatal("calendar_failure_became_mail_diagnostic")
	}
	for _, mutate := range []func(*CalendarQualificationFailure){
		func(d *CalendarQualificationFailure) { d.Version++ },
		func(d *CalendarQualificationFailure) { d.Kind = CalendarObservationKind },
		func(d *CalendarQualificationFailure) { d.Owner = validOwner() },
		func(d *CalendarQualificationFailure) { d.Owner.Adopted, d.Owner.Child = false, Process{} },
		func(d *CalendarQualificationFailure) { d.Stage = "synthetic-private" },
		func(d *CalendarQualificationFailure) { d.Reason = "synthetic-private" },
		func(d *CalendarQualificationFailure) { d.Fields = []string{"synthetic-private"} },
		func(d *CalendarQualificationFailure) { d.Fields = []string{"is_private", "is_private"} },
		func(d *CalendarQualificationFailure) { d.Fields = nil },
		func(d *CalendarQualificationFailure) { d.Omissions = nil },
		func(d *CalendarQualificationFailure) { d.Omissions["synthetic-private"] = 1 },
	} {
		value := calendarFailure()
		mutate(&value)
		wire, _ := Encode(value)
		if _, err := DecodeCalendarQualificationFailure(wire); err == nil || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("foreign_or_private_failure_packet_accepted_or_disclosed")
		}
	}
	value.Owner.Child.Created++
	if MatchCalendarQualificationFailure(calendarQualificationOwner(), value) == nil {
		t.Fatal("stale_child_failure_matched")
	}
	value.Fields = []string{strings.Repeat("synthetic-private", MaxRecordBytes)}
	if MatchCalendarQualificationFailure(calendarQualificationOwner(), value) == nil {
		t.Fatal("oversized_failure_matched")
	}
}

func TestCalendarQualificationFailureReasonsAreStageBound(t *testing.T) {
	for _, row := range []struct{ stage, reason string }{
		{StageCapture, "snapshot_refused"}, {StageCensus, "census_close_refused"},
		{StageCalendarOracle, "calendar_oracle_html_semantics_unproved"},
		{StageCalendarOracle, "calendar_oracle_regional_zone_unsupported"},
		{StageCalendarOracle, "calendar_survey_close_refused"},
		{StageCalendarSQL, "archive_mismatch"}, {StageCalendarSQL, "calendar_oracle_persisted_zone_spelling_unproved"},
		{StageCalendarHistory, "repeat_archive_unstable"}, {StageCleanup, "archive_close_failed"},
	} {
		value := calendarFailure()
		value.Stage, value.Reason, value.Fields = row.stage, row.reason, []string{}
		wire, _ := Encode(value)
		if _, err := DecodeCalendarQualificationFailure(wire); err != nil {
			t.Fatal("fixed_stage_bound_failure_refused")
		}
		if FixedCalendarQualificationFailureReason(row.stage, row.reason) != row.reason {
			t.Fatal("fixed_failure_reason_not_preserved")
		}
	}
	for _, code := range []string{"synthetic-private", strings.Repeat("private", 1000)} {
		if FixedCalendarQualificationFailureReason(StageCalendarOracle, code) != "unproved" {
			t.Fatal("arbitrary_error_text_forwarded")
		}
	}
	joined := errors.Join(errors.New("calendar_survey_records_refused"), errors.New("calendar_oracle_html_semantics_unproved"), errors.New("calendar_oracle_read_unproved"))
	if CalendarQualificationOracleReason(joined) != "calendar_oracle_html_semantics_unproved" ||
		CalendarQualificationOracleReason(errors.Join(errors.New("calendar_survey_layout_refused"), errors.New("calendar_oracle_read_unproved"))) != "calendar_survey_layout_refused" ||
		CalendarQualificationOracleReason(errors.New("synthetic-private")) != "unproved" ||
		CalendarQualificationOracleReason(errors.New(strings.Repeat("private", 1000))) != "unproved" ||
		CalendarQualificationOracleReason(context.Canceled) != "calendar_oracle_context_refused" ||
		CalendarQualificationOracleReason(context.DeadlineExceeded) != "calendar_oracle_context_refused" ||
		CalendarQualificationOracleReason(nil) != "unproved" {
		t.Fatal("bounded_oracle_reason_selection_failed_or_disclosed")
	}
	if CalendarQualificationFailureFields("synthetic-private", []string{}) {
		t.Fatal("unknown_failure_stage_accepted_even_without_fields")
	}
	if CalendarQualificationFailureFields(StageCalendarSQL, nil) || CalendarQualificationFailureFields(StageCalendarSQL, make([]string, 100)) {
		t.Fatal("missing_or_unbounded_failure_field_set_accepted")
	}
	value := calendarFailure()
	value.Stage, value.Reason, value.Fields = StageCalendarHistory, "archive_mismatch", []string{}
	wire, _ := Encode(value)
	if _, err := DecodeCalendarQualificationFailure(wire); err == nil {
		t.Fatal("failure_reason_moved_to_unrelated_stage")
	}
}
