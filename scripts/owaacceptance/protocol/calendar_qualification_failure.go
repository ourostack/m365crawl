package protocol

import (
	"context"
	"errors"
	"slices"
	"strings"
)

const (
	CalendarQualificationFailureName = "calendar-qualification-failure.json"
	CalendarQualificationFailureKind = "owa_calendar_product_sql_failure"
	StageCalendarOracle              = "calendar_oracle"
	StageCalendarSQL                 = "calendar_sql"
	StageCalendarHistory             = "calendar_history"
)

type CalendarQualificationFailure struct {
	Version   int              `json:"version"`
	Kind      string           `json:"kind"`
	Owner     Ownership        `json:"owner"`
	Stage     string           `json:"stage"`
	Reason    string           `json:"reason"`
	Fields    []string         `json:"fields"`
	Omissions map[string]int64 `json:"omissions"`
}

var calendarOracleFailureReasons = strings.Fields(`
	calendar_oracle_context_refused
	calendar_oracle_object_refused calendar_oracle_identity_refused calendar_oracle_time_refused
	calendar_oracle_text_budget_refused calendar_oracle_text_budget_or_encoding_refused calendar_oracle_attendee_budget_refused
	calendar_oracle_structured_privacy_unproved calendar_oracle_bearer_whitespace_unproved
	calendar_oracle_json_budget_refused calendar_oracle_json_decode_refused calendar_oracle_json_encode_refused
	calendar_oracle_json_key_privacy_unproved calendar_oracle_json_span_refused
	calendar_oracle_html_encoding_or_budget_refused calendar_oracle_html_token_budget_refused
	calendar_oracle_html_lexical_unproved calendar_oracle_html_semantics_unproved calendar_oracle_html_output_budget_refused
	calendar_oracle_alias_policy_unproved calendar_oracle_zone_unknown calendar_oracle_zone_unmapped
	calendar_oracle_icu_refused calendar_oracle_native_refused calendar_oracle_zone_budget_refused
	calendar_oracle_regional_zone_unsupported calendar_oracle_native_year_refused
	calendar_oracle_read_unproved calendar_oracle_decode_coverage_refused calendar_oracle_duplicate_identity_conflict
	calendar_oracle_persisted_zone_spelling_unproved calendar_oracle_attendee_encoding_refused
	calendar_oracle_expected_read_unproved calendar_oracle_expected_identity_conflict
	calendar_oracle_archive_query_refused calendar_oracle_archive_shape_refused
	calendar_survey_context_refused calendar_survey_source_refused calendar_survey_metadata_refused calendar_survey_layout_refused
	calendar_survey_open_refused calendar_survey_resource_refused calendar_survey_records_refused calendar_survey_close_refused
	calendar_source_volume_refused`)

var calendarSQLFailureFields = strings.Fields(`
	source account_id source_id event_key composite_key global_id ical_uid series_key event_type original_start
	start_at end_at all_day start_date end_date time_zone time_zone_iana utc_offset subject organizer organizer_address
	is_organizer is_private cancelled response show_as is_online_meeting location last_modified online_meeting_url
	short_join_url dial_in_conference_id dial_in_toll_number teams_thread_id attendees_json locations_json
	body_html body_text body_type body_preview attachments_json has_attachments categories_json recurrence_json
	reminder_minutes detail_raw_json unknown_fields removed_at event_census`)

func CalendarQualificationFailureStage(stage string) bool {
	return DiagnosticStage(stage) || stage == StageCalendarOracle || stage == StageCalendarSQL || stage == StageCalendarHistory
}

func FixedCalendarQualificationFailureReason(stage, reason string) string {
	if DiagnosticStage(stage) {
		return FixedDiagnosticReason(stage, reason)
	}
	if (stage == StageCalendarOracle || stage == StageCalendarSQL) && slices.Contains(calendarOracleFailureReasons, reason) ||
		stage == StageCalendarSQL && reason == "archive_mismatch" ||
		stage == StageCalendarHistory && (reason == "repeat_archive_unstable" || reason == "archive_state_refused") {
		return reason
	}
	return "unproved"
}

func CalendarQualificationFailureFields(stage string, fields []string) bool {
	if stage == StageImport {
		return calendarImportFailureFields(fields)
	}
	if DiagnosticStage(stage) {
		return DiagnosticFields(fields)
	}
	if stage != StageCalendarOracle && stage != StageCalendarSQL && stage != StageCalendarHistory {
		return false
	}
	if fields == nil || len(fields) > len(calendarSQLFailureFields) {
		return false
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if stage != StageCalendarSQL || seen[field] || !slices.Contains(calendarSQLFailureFields, field) {
			return false
		}
		seen[field] = true
	}
	return true
}

// Only complete fixed codes are selected; joined errors are bounded and private text is never forwarded.
func CalendarQualificationOracleReason(err error) string {
	if err == nil {
		return "unproved"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "calendar_oracle_context_refused"
	}
	text := err.Error()
	if len(text) > 1024 || strings.Count(text, "\n") > 8 {
		return "unproved"
	}
	fallback := "unproved"
	for _, code := range strings.Split(text, "\n") {
		if slices.Contains(calendarOracleFailureReasons, code) {
			if strings.HasPrefix(code, "calendar_oracle_") && code != "calendar_oracle_read_unproved" {
				return code
			}
			if code != "calendar_oracle_read_unproved" || fallback == "unproved" {
				fallback = code
			}
		}
	}
	return fallback
}

func DecodeCalendarQualificationFailure(data []byte) (CalendarQualificationFailure, error) {
	var value CalendarQualificationFailure
	if strict(data, &value) != nil || value.Version != Version || value.Kind != CalendarQualificationFailureKind ||
		validateOwnershipTest(value.Owner, CalendarQualificationTest) != nil || !value.Owner.Adopted ||
		!CalendarQualificationFailureStage(value.Stage) ||
		value.Reason != "unproved" && FixedCalendarQualificationFailureReason(value.Stage, value.Reason) != value.Reason ||
		!CalendarQualificationFailureFields(value.Stage, value.Fields) ||
		!calendarQualificationOmissions(value.Stage, value.Reason, value.Omissions) {
		return CalendarQualificationFailure{}, calendarQualificationRefused()
	}
	return value, nil
}

func MatchCalendarQualificationFailure(owner Ownership, value CalendarQualificationFailure) error {
	_, err := calendarQualificationFailureData(owner, value)
	return err
}

func calendarQualificationFailureData(owner Ownership, value CalendarQualificationFailure) ([]byte, error) {
	data, err := Encode(value)
	if err != nil {
		return nil, calendarQualificationRefused()
	}
	if _, err := DecodeCalendarQualificationFailure(data); err != nil || owner != value.Owner {
		return nil, calendarQualificationRefused()
	}
	return data, nil
}
