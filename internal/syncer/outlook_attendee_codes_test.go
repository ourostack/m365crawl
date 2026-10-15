package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeFixedFailureCodes(t *testing.T) {
	for _, c := range []struct {
		cause error
		code  string
	}{
		{store.ErrAttendeeArchiveCorrupt, "outlook_attendee_archive_corrupt"},
		{outlookmeetingattendees.ErrTooLarge, "outlook_attendee_evidence_too_large"},
		{outlookmeetingattendees.ErrInvalid, "outlook_attendee_evidence_invalid"},
		{errPeopleCleanup, "outlook_attendee_cleanup_failed"},
		{context.Canceled, "outlook_attendee_cancelled"},
		{&outlookattendees.Error{Code: "outlook_attendees_layout_unsupported"}, "outlook_attendees_layout_unsupported"},
		{errors.New("outlook_attendee_selection_too_large"), "outlook_attendee_selection_too_large"},
		{errors.New("SYNTHETIC-PRIVATE"), "outlook_attendees_read_failed"},
	} {
		if got := attendeeCode(c.cause); got != c.code {
			t.Errorf("failure code: got %q want %q", got, c.code)
		}
	}
}
