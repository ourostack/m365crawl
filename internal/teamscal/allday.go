package teamscal

import (
	"math"
	"time"
)

// allDayDates derives the date span of an all-day event from its instants. The cache has no
// all-day event yet, so the shape is assumed (A5): startTime is midnight of the start date in the
// event's zone, or in UTC. The zones are tried in that order, then fallback (the zone the sync
// groups days in). The first in which start is exactly midnight gives the start date; ok is false
// when none does, and the caller stores the event as timed. The end date is exclusive: the start
// date plus the span rounded to whole days (rounding, not truncating, so a span across a
// daylight-saving change still counts its days), at least one.
func allDayDates(start, end time.Time, eventZone, fallback *time.Location) (startDate, endDate string, ok bool) {
	if start.IsZero() {
		return "", "", false
	}
	for _, loc := range []*time.Location{eventZone, time.UTC, fallback} {
		if loc == nil {
			continue
		}
		s := start.In(loc)
		if s.Hour() != 0 || s.Minute() != 0 || s.Second() != 0 || s.Nanosecond() != 0 {
			continue
		}
		days := max(1, int(math.Round(end.Sub(start).Hours()/24)))
		return s.Format(time.DateOnly), s.AddDate(0, 0, days).Format(time.DateOnly), true
	}
	return "", "", false
}
