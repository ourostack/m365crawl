package calendar

import (
	"sort"
	"time"
)

// Merge picks the freshest copy among rows that share a key and fills its empty text fields from
// the others. The base is the row with the newest LastModified; on a tie or a missing time, the
// row whose source has the newer fresh time; then Outlook, the calendar's home client. Fields fill
// in the same order. SourceID, StartDate and EndDate belong to the base row and are never filled,
// and Cancelled counts only when the base row says so, so a stale cancellation never wins.
func Merge(rows []Event, fresh map[Source]time.Time) Event {
	if len(rows) == 0 {
		return Event{}
	}
	ordered := append([]Event(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return better(ordered[i], ordered[j], fresh) })
	base := ordered[0]
	for _, other := range ordered[1:] {
		fillEmpty(&base, other)
	}
	return base
}

// better reports whether a outranks b. It is a total order only for at most two rows per key (one
// per source), which is all the calendar holds; with more rows the tie-breaks could cycle.
func better(a, b Event, fresh map[Source]time.Time) bool {
	if a.LastModified != nil && b.LastModified != nil && !a.LastModified.Equal(*b.LastModified) {
		return a.LastModified.After(*b.LastModified)
	}
	if fa, fb := fresh[a.Source], fresh[b.Source]; !fa.Equal(fb) {
		return fa.After(fb)
	}
	return a.Source == SourceOutlook && b.Source != SourceOutlook
}

// fillEmpty copies into base each text field that base leaves empty and other has.
func fillEmpty(base *Event, other Event) {
	for _, f := range []struct{ dst, src *string }{
		{&base.GlobalID, &other.GlobalID},
		{&base.TimeZone, &other.TimeZone},
		{&base.Subject, &other.Subject},
		{&base.Organizer, &other.Organizer},
		{&base.AttendeesJSON, &other.AttendeesJSON},
		{&base.Location, &other.Location},
		{&base.OnlineMeetingURL, &other.OnlineMeetingURL},
		{&base.TeamsThreadID, &other.TeamsThreadID},
		{&base.SeriesKey, &other.SeriesKey},
		{&base.Response, &other.Response},
		{&base.ShowAs, &other.ShowAs},
		{&base.BodyPreview, &other.BodyPreview},
	} {
		if *f.dst == "" {
			*f.dst = *f.src
		}
	}
}
