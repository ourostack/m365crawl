package outlookowacal

import (
	"encoding/json"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestExplicitUnknownResponsePreservesUncertaintyWithoutObservationLoss(t *testing.T) {
	for _, response := range []string{"Unknown", "NoResponseReceived", "Synthetic Unsupported"} {
		t.Run(response, func(t *testing.T) {
			value := event()
			replace(value, "ResponseType", response)
			replace(value, "RequiredAttendees", []any{object("Mailbox", object("EmailAddress", "synthetic@example.invalid"), "ResponseType", response)})
			replace(value, "OptionalAttendees", []any{})
			replace(value, "Resources", []any{})
			got, notes, err := MapEvent("synthetic-account", value)
			if err != nil {
				t.Fatal("synthetic_response_event_refused")
			}
			var attendees []calendarAttendee
			if json.Unmarshal([]byte(got.AttendeesJSON), &attendees) != nil || len(attendees) != 1 || unknown(got, calendar.FieldAttendees) {
				t.Fatal("response_uncertainty_erased_observed_attendee")
			}
			switch response {
			case "Unknown":
				if notes.Unmapped != 0 || got.Response != "" || attendees[0].Response != "" || !unknown(got, calendar.FieldResponse) {
					t.Fatal("explicit_unknown_response_misclassified_as_observation_loss")
				}
			case "NoResponseReceived":
				if notes.Unmapped != 0 || got.Response != "none" || attendees[0].Response != "none" || unknown(got, calendar.FieldResponse) {
					t.Fatal("no_response_received_conflated_with_unknown")
				}
			default:
				if notes.Unmapped != 2 || got.Response != "" || !unknown(got, calendar.FieldResponse) {
					t.Fatal("unsupported_response_omissions_suppressed")
				}
			}
		})
	}
}
