//go:build acceptance && windows

package acceptance

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func TestOWACalendarIndependentThreadLiterals(t *testing.T) {
	const thread = "19:meeting_SYNTHETIC-123@thread.v2"
	for _, tc := range []struct{ source, expected string }{
		{"https://teams.microsoft.com/l/meetup-join/19%3Ameeting_SYNTHETIC-123%40thread.v2/0", thread},
		{"unrelated%invalid/" + thread, thread},
		{"19%3Ameeting_SYNTHETIC-123%40thread.v2%invalid", ""},
		{"https://example.invalid/19:meeting_@thread.v2", ""},
		{"https://example.invalid/19:meeting_BAD+ID@thread.v2", ""},
		{"19:meeting_FIRST@thread.v2 then " + thread, "19:meeting_FIRST@thread.v2"},
	} {
		if got := owaOracleMeetingThread(tc.source); got != tc.expected {
			t.Fatal("independent_thread_literal_mismatch")
		}
	}
}

func TestOWACalendarIndependentEventKnowledge(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	value := owaTemporalLiteral()
	owaTemporalSet(value, "Subject", "")
	owaTemporalSet(value, "OnlineMeetingJoinUrl", "https://teams.microsoft.com/l/meetup-join/19%3Ameeting_SYNTHETIC%40thread.v2/0")
	owaTemporalSet(value, "Body", owaObject("BodyType", "Text", "Value", ""))
	for _, key := range []string{"RequiredAttendees", "OptionalAttendees", "Resources"} {
		owaTemporalSet(value, key, []any{})
	}
	series := strings.Repeat("0", 32) + "00000000aa"
	owaTemporalSet(value, "UID", strings.Repeat("0", 32)+"07ef0306aa")
	expected, err := owaProjectEvent(t.Context(), oracle, value)
	wantUnknown := []string{"attachments", "body_preview", "categories", "dial_in", "location", "organizer",
		"organizer_address", "recurrence", "reminder", "response", "rooms", "short_join_url", "show_as", "utc_offset"}
	if err != nil || expected.Series != series || expected.Thread != "19:meeting_SYNTHETIC@thread.v2" ||
		len(expected.Known) != 21 || !reflect.DeepEqual(expected.Unknown, wantUnknown) {
		t.Fatal("independent_event_identity_or_knowledge_literal_mismatch")
	}
	actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
	var actualUnknown []string
	for _, field := range actual.Unknown {
		actualUnknown = append(actualUnknown, string(field))
	}
	if err != nil || actual.SeriesKey != series || actual.TeamsThreadID != expected.Thread ||
		!actual.UnknownDeclared || !reflect.DeepEqual(actualUnknown, wantUnknown) ||
		notes.Unmapped != expected.Invalid || notes.UnknownZone != expected.UnknownZones ||
		notes.AllDayUnaligned != expected.Unaligned || notes.PartialAttendees != expected.PartialAttendees || notes.Redacted != expected.Redacted {
		t.Fatal("full_event_knowledge_or_uncertainty_differential_mismatch")
	}
	for _, uid := range []string{"aa11", strings.Repeat("0", 32) + "07ef0230aa", series} {
		if got := owaOracleSeries(uid); got != uid {
			t.Fatal("invalid_or_absent_occurrence_date_created_series_identity")
		}
	}
}

func TestOWACalendarIndependentEventZonePrivacy(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	value := owaTemporalLiteral()
	owaTemporalSet(value, "StartTimeZoneId", "Bearer SYNTHETIC")
	expected, err := owaProjectEvent(t.Context(), oracle, value)
	actual, notes, actualErr := outlookowacal.MapEvent("outlook/Synthetic", value)
	if err != nil || actualErr != nil || expected.Temporal.Zone != "[redacted]" || actual.TimeZone != "[redacted]" ||
		expected.Redacted != 1 || notes.Redacted != 1 || expected.UnknownZones != 1 || expected.Unaligned != 1 {
		t.Fatal("zone_privacy_evidence_or_counts_mismatch")
	}
}

func TestOWACalendarIndependentEventAllUnknownCoverage(t *testing.T) {
	oracle, err := owaNewWindowsZoneOracle(t.Context())
	if err != nil {
		t.Fatal("synthetic_oracle_setup_refused")
	}
	value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
		"LastModifiedTime", "2026-10-19T00:00:00Z")
	expected, err := owaProjectEvent(t.Context(), oracle, value)
	actual, _, actualErr := outlookowacal.MapEvent("outlook/Synthetic", value)
	var unknown []string
	for _, field := range actual.Unknown {
		unknown = append(unknown, string(field))
	}
	if err != nil || actualErr != nil || len(expected.Known) != 21 || len(expected.Unknown) != 21 ||
		!reflect.DeepEqual(expected.Unknown, unknown) || expected.Series != "aa11" || expected.Thread != "" {
		t.Fatal("missing_optional_evidence_lost_full_explicit_unknown_coverage")
	}
}
