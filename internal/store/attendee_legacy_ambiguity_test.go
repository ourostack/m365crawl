package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestAttendeeCalendarLegacyAmbiguity(t *testing.T) {
	s := newStore(t)
	one, two := outlookEvent(calendar.TriFalse, base), outlookEvent(calendar.TriFalse, base)
	two.SourceID, two.GlobalID, two.ICalUID, two.SeriesKey = "AA22", "AA22", "AA22", "AA22"
	commitOutlook(t, s, time.UTC, "same", one, two)
	if _, err := s.CalendarEvent(context.Background(), nil, calendar.Key(one)[:1]); err == nil || !strings.Contains(err.Error(), "matches 2 calendar events") {
		t.Fatal("legacy ambiguous reference precedence changed", err)
	}
}
