package protocol

import (
	"slices"
	"strings"
)

var calendarImportPasses = strings.Fields(`pass_initial pass_unchanged pass_full_read pass_unproved`)

var calendarImportCauses = strings.Fields(`
	source_reports mail_source calendar_source calendar_omissions mail_omissions calendar_error_unproved`)

var calendarSourceErrorCodes = strings.Fields(`
	outlook_layout_unsupported outlook_profile_unreadable outlook_store_unrecognized outlook_store_version
	db_error internal snapshot_inconsistent unsupported_block_compression`)

var calendarSourceLossCodes = strings.Fields(`
	outlook_event_unmapped calendar_refused outlook_owa_duplicate_conflict
	outlook_owa_reader_truncated_log_tail outlook_owa_source_changed`)

func CalendarQualificationSourceErrorField(code string) string {
	for _, literal := range calendarSourceErrorCodes {
		if code == literal {
			return "calendar_error_" + literal
		}
	}
	return "calendar_error_unproved"
}

func calendarImportFailureFields(fields []string) bool {
	if DiagnosticFields(fields) {
		return true
	}
	return len(fields) == 2 && slices.Contains(calendarImportPasses, fields[0]) &&
		(slices.Contains(calendarImportCauses, fields[1]) || calendarErrorField(fields[1]))
}

func calendarErrorField(field string) bool {
	for _, code := range calendarSourceErrorCodes {
		if field == "calendar_error_"+code {
			return true
		}
	}
	return false
}

func calendarSourceLosses(omissions map[string]int64) bool {
	if omissions == nil || len(omissions) > len(calendarSourceLossCodes) {
		return false
	}
	var total int64
	for code, count := range omissions {
		if !slices.Contains(calendarSourceLossCodes, code) || count <= 0 || count > MaxCount-total {
			return false
		}
		total += count
	}
	return true
}

func CalendarQualificationSourceOmissions(input map[string]int) (map[string]int64, bool) {
	if len(input) > len(calendarSourceLossCodes) {
		return map[string]int64{}, false
	}
	safe := map[string]int64{}
	for _, code := range calendarSourceLossCodes {
		if count, found := input[code]; found {
			safe[code] = int64(count)
		}
	}
	if len(safe) != len(input) || !calendarSourceLosses(safe) {
		return map[string]int64{}, false
	}
	return safe, true
}

func calendarQualificationOmissions(stage, reason string, omissions map[string]int64) bool {
	return DiagnosticOmissions(stage, reason, omissions) ||
		stage == StageImport && reason == "sync_source_refused" && calendarSourceLosses(omissions)
}
