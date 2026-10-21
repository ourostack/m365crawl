//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func owaOptionalLiteral() any {
	return owaObject(
		"Subject", "Synthetic subject", "Location", "", "Preview", "Synthetic preview",
		"OnlineMeetingJoinUrl", "https://example.invalid/meeting",
		"OnlineMeetingConferenceId", "", "ResponseType", "Accept", "FreeBusyType", "Busy",
		"CalendarItemType", "Single",
		"Organizer", owaObject("Mailbox", owaObject("Name", "Synthetic organizer", "EmailAddress", "organizer@example.invalid")),
		"RequiredAttendees", []any{owaObject("Mailbox", owaObject("Name", "Synthetic required", "EmailAddress", "required@example.invalid"), "ResponseType", "Tentative")},
		"OptionalAttendees", []any{}, "Resources", []any{owaObject("Mailbox", owaObject("EmailAddress", "room@example.invalid"))},
	)
}

func TestOWACalendarOptionalOracleLiterals(t *testing.T) {
	out, err := owaProjectOptional(t.Context(), owaOptionalLiteral())
	if err != nil || len(out.Fields) != 10 || out.Fields["subject"].Value != "Synthetic subject" ||
		!out.Fields["location"].Known || out.Fields["location"].Value != "" || !out.Fields["dial_in"].Known ||
		out.Fields["response"].Value != "accepted" || out.Fields["show_as"].Value != "busy" ||
		out.Fields["event_type"].Value != "single" || out.Fields["organizer_address"].Value != "organizer@example.invalid" ||
		!out.AttendeesKnown || out.Invalid != 0 || out.PartialAttendees != 0 {
		t.Fatal("optional_literal_evidence_mismatch")
	}
	want := []owaExpectedAttendee{
		{Name: "Synthetic required", Address: "required@example.invalid", Type: "required", Response: "tentative"},
		{Address: "room@example.invalid", Type: "resource"},
	}
	if !reflect.DeepEqual(out.Attendees, want) {
		t.Fatal("role_order_or_explicit_empty_group_lost")
	}
	for _, unsupported := range []string{"body", "meeting_chat_id", "attachments", "recurrence"} {
		if _, falselyCovered := out.Fields[unsupported]; falselyCovered {
			t.Fatal("unsupported_field_claimed_unknown_or_known")
		}
	}
}

func TestOWACalendarOptionalOracleUncertainty(t *testing.T) {
	for _, tc := range []struct {
		value            any
		invalid, partial int
	}{
		{owaObject(), 0, 0},
		{owaObject("Subject", 12.0), 1, 0},
		{owaObject("ResponseType", "Synthetic Unsupported"), 1, 0},
		{owaObject("FreeBusyType", "Synthetic Unsupported"), 1, 0},
		{owaObject("Organizer", false), 1, 0},
		{owaObject("Organizer", owaObject("Mailbox", false)), 1, 0},
		{owaObject("RequiredAttendees", []any{}), 0, 1},
		{owaObject("RequiredAttendees", false), 1, 0},
		{owaObject("RequiredAttendees", []any{owaObject("Mailbox", owaObject())}, "OptionalAttendees", []any{}, "Resources", []any{}), 1, 0},
	} {
		out, err := owaProjectOptional(t.Context(), tc.value)
		if err != nil || out.Invalid != tc.invalid || out.PartialAttendees != tc.partial || out.AttendeesKnown {
			t.Fatal("partial_or_invalid_details_became_explicit_empty")
		}
	}
	out, err := owaProjectOptional(t.Context(), owaObject("RequiredAttendees", []any{}, "OptionalAttendees", []any{}, "Resources", []any{}))
	if err != nil || !out.AttendeesKnown || out.Attendees == nil || len(out.Attendees) != 0 {
		t.Fatal("complete_empty_attendee_evidence_lost")
	}
	clean, err := owaProjectOptional(t.Context(), owaObject("Subject", "Bearer SYNTHETIC"))
	if err != nil || clean.Fields["subject"].Value != "[redacted]" || clean.Redacted != 1 {
		t.Fatal("independent_known_redaction_was_lost")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owaProjectOptional(ctx, owaOptionalLiteral()); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_optional_projection_accepted")
	}
}

func TestOWACalendarOptionalOracleSyntheticDifferential(t *testing.T) {
	for _, value := range []any{owaOptionalLiteral(), owaObject("Subject", "", "RequiredAttendees", []any{})} {
		expected, err := owaProjectOptional(t.Context(), value)
		if err != nil {
			t.Fatal("optional_golden_refused")
		}

		fields, _ := owaSurveyObject(value)
		raw := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z", "LastModifiedTime", "2026-10-19T00:00:00Z")
		for key, field := range fields {
			raw.Keys, raw.Values = append(raw.Keys, key), append(raw.Values, field)
		}
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", raw)
		if err != nil || notes.Unmapped != expected.Invalid || notes.PartialAttendees != expected.PartialAttendees || notes.Redacted != expected.Redacted {
			t.Fatal("optional_uncertainty_differential_mismatch")
		}
		if actual.DialInConferenceID != expected.DialConference.Value || actual.DialInTollNumber != expected.DialNumber.Value ||
			string(actual.EventType) != expected.Fields["event_type"].Value {
			t.Fatal("optional_dial_or_type_differential_mismatch")
		}
		for _, field := range []struct {
			key   string
			value string
			group calendar.Field
		}{
			{"subject", actual.Subject, calendar.FieldSubject}, {"location", actual.Location, calendar.FieldLocation},
			{"body_preview", actual.BodyPreview, calendar.FieldBodyPreview}, {"join_url", actual.OnlineMeetingURL, calendar.FieldJoinURL},
			{"organizer", actual.Organizer, calendar.FieldOrganizer}, {"organizer_address", actual.OrganizerAddress, calendar.FieldOrganizerAddress},
			{"response", actual.Response, calendar.FieldResponse}, {"show_as", actual.ShowAs, calendar.FieldShowAs},
			{"dial_in", "", calendar.FieldDialIn},
		} {
			want := expected.Fields[field.key]
			unknown := false
			for _, entry := range actual.Unknown {
				unknown = unknown || entry == field.group
			}
			if field.value != want.Value || unknown == want.Known {
				t.Fatal("optional_value_or_knowledge_differential_mismatch")
			}
		}
		if expected.AttendeesKnown {
			var actualRows []owaExpectedAttendee
			if json.Unmarshal([]byte(actual.AttendeesJSON), &actualRows) != nil || !reflect.DeepEqual(actualRows, expected.Attendees) {
				t.Fatal("attendee_rows_differential_mismatch")
			}
		} else if actual.AttendeesJSON != "" {
			t.Fatal("unknown_attendees_became_empty_removal")
		}
	}
}

func TestOWACalendarOptionalOracleAllEnumLiterals(t *testing.T) {
	for _, tc := range []struct{ source, raw, target, literal string }{
		{"ResponseType", "Accept", "response", "accepted"},
		{"ResponseType", "Tentative", "response", "tentative"},
		{"ResponseType", "Decline", "response", "declined"},
		{"ResponseType", "NoResponseReceived", "response", "none"},
		{"ResponseType", "Organizer", "response", "organizer"},
		{"FreeBusyType", "Free", "show_as", "free"},
		{"FreeBusyType", "Tentative", "show_as", "tentative"},
		{"FreeBusyType", "Busy", "show_as", "busy"},
		{"CalendarItemType", "Single", "event_type", "single"},
		{"CalendarItemType", "Occurrence", "event_type", "occurrence"},
		{"CalendarItemType", "Exception", "event_type", "exception"},
		{"CalendarItemType", "RecurringMaster", "event_type", "master"},
	} {
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", tc.source, tc.raw)
		expected, err := owaProjectOptional(t.Context(), value)
		if err != nil || !expected.Fields[tc.target].Known || expected.Fields[tc.target].Value != tc.literal || expected.Invalid != 0 {
			t.Fatal("optional_enum_literal_projection_mismatch")
		}
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		values := map[string]string{"response": actual.Response, "show_as": actual.ShowAs, "event_type": string(actual.EventType)}
		if err != nil || notes.Unmapped != 0 || values[tc.target] != tc.literal {
			t.Fatal("optional_enum_literal_differential_mismatch")
		}
	}
}
