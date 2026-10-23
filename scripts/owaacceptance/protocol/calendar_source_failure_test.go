package protocol

import (
	"strings"
	"testing"
)

func TestCalendarSourceFailureKeepsClosedPassCauseAndLoss(t *testing.T) {
	value := calendarFailure()
	value.Stage, value.Reason = StageImport, "sync_source_refused"
	value.Fields = []string{"pass_initial", "calendar_omissions"}
	value.Omissions = map[string]int64{"outlook_event_unmapped": 7}
	data, _ := Encode(value)
	if _, err := DecodeCalendarQualificationFailure(data); err != nil {
		t.Fatal("calendar_source_diagnostic_not_preserved")
	}

	if DiagnosticFields(value.Fields) || DiagnosticOmissions(value.Stage, value.Reason, value.Omissions) {
		t.Fatal("calendar_source_diagnostic_broadened_legacy_mail_scope")
	}
	for _, mutate := range []func(*CalendarQualificationFailure){
		func(v *CalendarQualificationFailure) { v.Fields[1] = "calendar_error_synthetic-private" },
		func(v *CalendarQualificationFailure) { v.Fields = []string{"pass_initial", "pass_initial"} },
		func(v *CalendarQualificationFailure) { v.Stage = StageCalendarSQL },
		func(v *CalendarQualificationFailure) { v.Reason = "sync_status_mismatch" },
		func(v *CalendarQualificationFailure) { v.Omissions["synthetic-private"] = 1 },
		func(v *CalendarQualificationFailure) { v.Omissions["outlook_event_unmapped"] = 0 },
		func(v *CalendarQualificationFailure) { v.Omissions["outlook_event_unmapped"] = MaxCount + 1 },
	} {
		original, _ := DecodeCalendarQualificationFailure(data)
		mutate(&original)
		wire, _ := Encode(original)
		if _, err := DecodeCalendarQualificationFailure(wire); err == nil {
			t.Fatal("calendar_source_diagnostic_escaped_closed_scope")
		}
	}
}

func TestCalendarSourceDiagnosticVocabularyAndBudgets(t *testing.T) {
	legacy := calendarFailure()
	legacy.Stage, legacy.Reason = StageImport, "sync_source_refused"
	legacy.Fields = []string{"mail_coverage"}
	legacy.Omissions = map[string]int64{"outlook_owa_detail_conflict": 2}
	wire, _ := Encode(legacy)
	if _, err := DecodeCalendarQualificationFailure(wire); err != nil ||
		!CalendarQualificationFailureFields(StageImport, []string{}) {
		t.Fatal("legacy_import_diagnostic_changed")
	}
	for _, code := range calendarSourceErrorCodes {
		field := CalendarQualificationSourceErrorField(code)
		fields := []string{"pass_initial", field}
		if field != "calendar_error_"+code || !CalendarQualificationFailureFields(StageImport, fields) ||
			CalendarQualificationFailureFields(StageAccounts, fields) || DiagnosticFields(fields) {
			t.Fatal("source_error_code_not_exact_or_scope_bound")
		}

	}
	for _, code := range []string{"synthetic-private", "internal\nsynthetic-private", strings.Repeat("private", MaxRecordBytes)} {
		if CalendarQualificationSourceErrorField(code) != "calendar_error_unproved" {
			t.Fatal("private_source_error_code_forwarded")
		}
	}
	for _, pass := range calendarImportPasses {
		for _, cause := range calendarImportCauses {
			if !CalendarQualificationFailureFields(StageImport, []string{pass, cause}) {
				t.Fatal("literal_import_field_refused")
			}
		}
	}
	if CalendarQualificationFailureFields(StageImport, nil) ||
		CalendarQualificationFailureFields(StageImport, make([]string, 200)) ||
		CalendarQualificationFailureFields(StageImport, []string{"calendar_error_synthetic-private"}) {
		t.Fatal("unbounded_or_private_import_fields_accepted")
	}
	for _, code := range calendarSourceLossCodes {
		got, valid := CalendarQualificationSourceOmissions(map[string]int{code: 2})
		if !valid || got[code] != 2 || !calendarQualificationOmissions(StageImport, "sync_source_refused", got) ||
			calendarQualificationOmissions(StageCalendarSQL, "sync_source_refused", got) {
			t.Fatal("literal_calendar_source_loss_refused_or_misplaced")
		}
	}
	for _, input := range []map[string]int{
		{"synthetic-private": 1}, {"outlook_event_unmapped": 0}, {"calendar_refused": -1},
		{"outlook_event_unmapped": MaxCount, "calendar_refused": 1},
		{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1, "f": 1},
	} {
		got, valid := CalendarQualificationSourceOmissions(input)
		if valid || len(got) != 0 {
			t.Fatal("invalid_calendar_loss_map_partially_used")
		}
	}
	if calendarSourceLosses(nil) || calendarSourceLosses(map[string]int64{"synthetic-private": 1}) ||
		calendarSourceLosses(map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1, "f": 1}) {
		t.Fatal("unbounded_calendar_loss_map_accepted")
	}
}

func TestCalendarSourceDiagnosticRequiresOnePassAndOneCause(t *testing.T) {
	for _, fields := range [][]string{
		{"pass_initial"}, {"calendar_omissions"}, {"pass_initial", "pass_full_read"},
		{"calendar_omissions", "pass_initial"}, {"pass_initial", "calendar_omissions", "mail_omissions"},
		{"pass_initial", "subject"},
	} {
		if CalendarQualificationFailureFields(StageImport, fields) {
			t.Fatal("calendar_import_diagnostic_accepted_ambiguous_pass_or_cause")
		}
	}
}
