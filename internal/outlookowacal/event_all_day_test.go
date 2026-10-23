package outlookowacal

import (
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestMapEventAllDayUsesDeclaredLocalDates(t *testing.T) {
	for _, tc := range []struct {
		name, zone, start, end, startDate, endDate string
	}{
		{"pacific", "Pacific Standard Time", "2026-10-20T07:00:00Z", "2026-10-21T07:00:00Z", "2026-10-20", "2026-10-21"},
		{"pacific_spring_dst", "Pacific Standard Time", "2026-03-08T08:00:00Z", "2026-03-09T07:00:00Z", "2026-03-08", "2026-03-09"},
		{"pacific_fall_dst", "America/Los_Angeles", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z", "2026-11-01", "2026-11-02"},
		{"multiple_local_days", "Pacific Standard Time", "2026-03-07T08:00:00Z", "2026-03-10T07:00:00Z", "2026-03-07", "2026-03-10"},
		{"fractional_offset", "India Standard Time", "2026-10-19T18:30:00Z", "2026-10-20T18:30:00Z", "2026-10-20", "2026-10-21"},
		{"eastern_positive_offset", "Tokyo Standard Time", "2026-10-19T15:00:00Z", "2026-10-20T15:00:00Z", "2026-10-20", "2026-10-21"},
		{"southern_dst", "AUS Eastern Standard Time", "2026-10-03T14:00:00Z", "2026-10-04T13:00:00Z", "2026-10-04", "2026-10-05"},
		{"utc", "UTC", "2026-10-20T00:00:00Z", "2026-10-22T00:00:00Z", "2026-10-20", "2026-10-22"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := event()
			replace(input, "IsAllDayEvent", true)
			replace(input, "StartTimeZoneId", tc.zone)
			replace(input, "Start", tc.start)
			replace(input, "End", tc.end)
			got, notes, err := MapEvent("outlook/Synthetic", input)
			if err != nil || !got.AllDay.Is(true) || got.StartDate != tc.startDate || got.EndDate != tc.endDate || notes != (Notes{}) {
				t.Fatalf("declared local all-day dates lost: all_day=%v dates=%s/%s notes=%+v err=%v", got.AllDay, got.StartDate, got.EndDate, notes, err)
			}
			if got.Start.Format(time.RFC3339Nano) != tc.start || got.End.Format(time.RFC3339Nano) != tc.end || got.TimeZone != tc.zone {
				t.Fatal("all-day conversion replaced source instants or zone")
			}
			calendar.AssertEveryFieldClassified(t, got)
			if err := calendar.ValidateEvent(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMapEventAllDayCannotGuessZoneOrWholeDayShape(t *testing.T) {
	for _, tc := range []struct {
		name, start, end string
		zone, endZone    any
		unknown, bad     int
	}{
		{"missing_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", nil, nil, 0, 0},
		{"unknown_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "Synthetic Unknown Zone", nil, 1, 0},
		{"host_local_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "Local", nil, 1, 0},
		{"malformed_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", 123.0, nil, 0, 1},
		{"utc_midnight_not_local", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "Pacific Standard Time", nil, 0, 0},
		{"wrong_start", "2026-10-20T08:00:00Z", "2026-10-21T07:00:00Z", "Pacific Standard Time", nil, 0, 0},
		{"wrong_end", "2026-10-20T07:00:00Z", "2026-10-21T08:00:00Z", "Pacific Standard Time", nil, 0, 0},
		{"fractional_midnight", "2026-10-20T07:00:00Z", "2026-10-21T07:00:00.000000001Z", "Pacific Standard Time", nil, 0, 0},
		{"zero_days", "2026-10-20T07:00:00Z", "2026-10-20T07:00:00Z", "Pacific Standard Time", nil, 0, 0},
		{"local_year_overflow", "9999-12-30T10:00:00Z", "9999-12-31T10:00:00Z", "Pacific/Kiritimati", nil, 0, 0},
		{"conflicting_end_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "UTC", "Atlantic/Reykjavik", 0, 0},
		{"unknown_end_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "UTC", "Synthetic Unknown Zone", 1, 0},
		{"malformed_end_zone", "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z", "UTC", 123.0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := event()
			replace(input, "IsAllDayEvent", true)
			replace(input, "StartTimeZoneId", tc.zone)
			replace(input, "EndTimeZoneId", tc.endZone)
			replace(input, "Start", tc.start)
			replace(input, "End", tc.end)
			got, notes, err := MapEvent("outlook/Synthetic", input)
			if err != nil || got.AllDay.Known() || got.StartDate != "" || got.EndDate != "" ||
				notes.AllDayUnaligned != 1 || notes.UnknownZone != tc.unknown || notes.Unmapped != tc.bad {
				t.Fatalf("unproved local dates accepted: all_day=%v dates=%s/%s notes=%+v err=%v", got.AllDay, got.StartDate, got.EndDate, notes, err)
			}
			calendar.AssertEveryFieldClassified(t, got)
			if err := calendar.ValidateEvent(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMapEventAllDayEndZoneUsesResolvedIdentity(t *testing.T) {
	input := event()
	replace(input, "IsAllDayEvent", true)
	replace(input, "Start", "2026-10-20T07:00:00Z")
	replace(input, "End", "2026-10-21T07:00:00Z")
	replace(input, "EndTimeZoneId", "America/Los_Angeles")
	got, notes, err := MapEvent("outlook/Synthetic", input)
	if err != nil || !got.AllDay.Is(true) || got.StartDate != "2026-10-20" || got.EndDate != "2026-10-21" || notes != (Notes{}) {
		t.Fatal("equivalent explicit Windows/IANA zone identities refused")
	}
}
