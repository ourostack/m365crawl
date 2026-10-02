// Package calendar is the calendar core shared by teamscrawl and outlookcrawl: the event model, the
// keys that identify one occurrence across sources, the merge that picks the freshest copy, and
// the per-source snapshot tables behind Agenda. It imports no source adapter and no archive code;
// callers hand it a *sql.DB opened with SchemaDDL.
package calendar

import (
	"fmt"
	"time"
)

// Source names a calendar source.
type Source string

// The calendar sources.
const (
	SourceTeams   Source = "teams"
	SourceOutlook Source = "outlook"
)

// Event is one occurrence of a calendar event as one source holds it.
type Event struct {
	Source   Source
	SourceID string
	// GlobalID is the id shared across sources (iCalUId or equivalent); empty when the source has none.
	GlobalID string
	// OriginalStart is the occurrence's original start (iCalendar RECURRENCE-ID); nil for a single
	// event. Moving an occurrence changes Start but not OriginalStart. For an all-day occurrence
	// the adapter passes midnight of the occurrence's own date in the event's time zone (or UTC
	// midnight of that date); Key takes the wall-clock date of the value as given.
	OriginalStart *time.Time
	Start, End    time.Time
	// AllDay events carry dates, never instants: StartDate and EndDate are YYYY-MM-DD, and EndDate
	// is exclusive (the iCalendar DTEND convention). An empty or non-later EndDate means one day.
	AllDay             bool
	StartDate, EndDate string
	TimeZone           string
	Subject, Organizer string
	AttendeesJSON      string
	Location           string
	OnlineMeetingURL   string
	TeamsThreadID      string
	SeriesKey          string
	Cancelled          bool
	Response, ShowAs   string
	BodyPreview        string
	LastModified       *time.Time
}

// Window is the range one source covered in a snapshot, and how fresh that snapshot was.
type Window struct {
	Source       Source
	Start, End   time.Time
	SyncedAt     time.Time
	CacheFreshAt time.Time
}

// timeLayout is fixed width so stored instants compare correctly as text (the layout the
// teamscrawl archive uses).
const timeLayout = "2006-01-02T15:04:05.000Z"

// dateLayout is the all-day date layout.
const dateLayout = time.DateOnly

// formatTime renders t as UTC text, or "" for the zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

// formatTimePtr renders t as UTC text, or nil (SQL NULL) for a nil pointer.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// timeText scans a stored instant: NULL and "" are the zero time, anything else must parse.
type timeText struct {
	t   time.Time
	set bool
}

// Scan implements sql.Scanner.
func (s *timeText) Scan(v any) error {
	*s = timeText{}
	if v == nil {
		return nil
	}
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("calendar: stored time has type %T, want text", v)
	}
	if str == "" {
		return nil
	}
	t, err := time.Parse(timeLayout, str)
	if err != nil {
		return fmt.Errorf("calendar: stored time %q: %w", str, err)
	}
	s.t, s.set = t, true
	return nil
}

// ptr returns the instant, or nil when none was stored.
func (s timeText) ptr() *time.Time {
	if !s.set {
		return nil
	}
	t := s.t
	return &t
}
