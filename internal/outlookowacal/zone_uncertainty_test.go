package outlookowacal

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

func TestCollectPreservesUnresolvedZoneWithoutInventingAnOmittedTimedEvent(t *testing.T) {
	value := event()
	replace(value, "StartTimeZoneId", "Synthetic Unresolved Zone")
	got, err := collect(context.Background(), &calendarReader{
		dbs: []indexeddb.Database{content()}, values: []any{value},
	}, "synthetic-account", true)
	if err != nil || got.Doubtful || len(got.Losses) != 0 || len(got.Events) != 1 || len(got.SeenIDs) != 1 ||
		got.Notes.UnknownZone != 1 || got.Notes.UnmappedEvents != 0 || got.Notes.UnmappedFields != 0 {
		t.Fatal("retained_unknown_zone_misclassified_as_missing_calendar_observation")
	}
	event := got.Events[0]
	if event.TimeZone != "Synthetic Unresolved Zone" || event.TimeZoneIANA != "" ||
		!unknown(event, calendar.FieldTimeZoneIANA) || !event.Start.Before(event.End) || event.AllDay.Is(true) {
		t.Fatal("unknown_zone_guessed_or_utc_interval_lost")
	}
}

func TestCollectUnknownZoneCannotMakeUnprovedAllDayDatesOrDamagedFieldsHealthy(t *testing.T) {
	for _, kind := range []string{"all_day", "damaged_subject", "unsupported_response"} {
		value := event()
		replace(value, "StartTimeZoneId", "Synthetic Unresolved Zone")
		switch kind {
		case "all_day":
			replace(value, "IsAllDayEvent", true)
		case "damaged_subject":
			replace(value, "Subject", 42.0)
		case "unsupported_response":
			replace(value, "ResponseType", "Synthetic Unsupported")
		}
		got, err := collect(context.Background(), &calendarReader{
			dbs: []indexeddb.Database{content()}, values: []any{value},
		}, "synthetic-account", true)
		if err != nil || !got.Doubtful || lossCount(got, outlookcal.CodeEventUnmapped) == 0 || got.Notes.UnknownZone != 1 {
			t.Fatal("real_observation_loss_suppressed_by_unknown_zone_handling")
		}
		if kind == "all_day" && (got.Events[0].AllDay.Known() || got.Events[0].StartDate != "" || got.Events[0].EndDate != "") {
			t.Fatal("unknown_zone_invented_local_all_day_dates")
		}
	}
}
