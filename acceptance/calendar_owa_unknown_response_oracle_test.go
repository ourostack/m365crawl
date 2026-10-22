//go:build acceptance

package acceptance

import "testing"

func TestOWAOptionalOracleDistinguishesExplicitUnknownResponse(t *testing.T) {
	value := owaObject(
		"ResponseType", "Unknown",
		"RequiredAttendees", []any{owaObject("Mailbox", owaObject("EmailAddress", "synthetic@example.invalid"), "ResponseType", "Unknown")},
		"OptionalAttendees", []any{}, "Resources", []any{},
	)
	got, err := owaProjectOptional(t.Context(), value)
	if err != nil || got.Invalid != 0 || got.Fields["response"].Known ||
		!got.AttendeesKnown || len(got.Attendees) != 1 || got.Attendees[0].Response != "" {
		t.Fatal("independent_explicit_unknown_response_became_loss_or_known_none")
	}
}
