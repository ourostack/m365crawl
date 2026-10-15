package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAttendeeAdmissionClockValidation(t *testing.T) {
	s := newStore(t)
	admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
	a := nativeAttempt(t, s)
	for _, field := range []string{"acquired", "admitted", "observed"} {
		c := AttendeeCapture{Evidence: attendeeModel(t, "B", 4), InputDigest: strings.Repeat("a", 64), CalendarReadID: 1, AcquiredAt: base, AdmittedAt: base}
		bad := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		switch field {
		case "acquired":
			c.AcquiredAt = bad
		case "admitted":
			c.AdmittedAt = bad
		case "observed":
			c.SourceObservedAt = &bad
		}
		if err := s.CommitAttendeeCapture(context.Background(), a, c); err == nil {
			t.Fatalf("unreadable %s clock admitted", field)
		}
	}
}
