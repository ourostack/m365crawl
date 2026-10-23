package outlookowacal

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookcal"
)

func TestChromiumCalendarWithoutZoneCannotInventAllDayDates(t *testing.T) {
	res, err := Collect(context.Background(), chromiumCalendar(t, "outlook-owa-calendar-fixture", "calendar"), "outlook/Synthetic", false)
	if err != nil || !res.Doubtful || len(res.Events) != 2 || res.Notes.AllDayUnaligned != 1 ||
		len(res.Losses) != 1 || res.Losses[0].Code != outlookcal.CodeEventUnmapped || res.Losses[0].Count != 1 {
		t.Fatal("original no-zone fixture lost valid rows or hid its all-day uncertainty")
	}
	event := res.Events[1]
	if event.SourceID != "bb22" || event.AllDay.Known() || event.StartDate != "" || event.EndDate != "" ||
		event.TimeZone != "" || event.TimeZoneIANA != "" ||
		event.Start.Format(time.RFC3339) != "2031-03-06T00:00:00Z" || event.End.Format(time.RFC3339) != "2031-03-07T00:00:00Z" {
		t.Fatal("UTC alignment was mistaken for declared local-date evidence")
	}
}
