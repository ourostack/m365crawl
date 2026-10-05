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

// Event types, as the sources report them.
const (
	EventSingle     = "single"
	EventOccurrence = "occurrence"
	EventException  = "exception"
	EventMaster     = "master"
)

// Event is one occurrence of a calendar event as one source holds it. Its fields fall in three
// groups that Capture treats differently: identity, schedule (compared against LastModified) and
// detail (compared against DetailAsOf). The bookkeeping fields at the end belong to the archive:
// adapters leave them zero, and Capture and the store maintain them.
type Event struct {
	// Identity.
	Source Source
	// AccountID partitions every calendar table, in the form "<tenantId>/<userId>"; empty only for a
	// source that has no account.
	AccountID string
	SourceID  string
	// GlobalID is the id shared across sources (iCalUId or equivalent); empty when the source has none.
	GlobalID string
	// ICalUID is the iCalendar uid the source knows. For Teams it equals GlobalID; it is the join
	// column to recaps.
	ICalUID string
	// SeriesKey is shared by a recurring master and its occurrences.
	SeriesKey string
	// EventType is one of the Event* constants; empty when the source did not say.
	EventType string
	// OriginalStart is the occurrence's original start (iCalendar RECURRENCE-ID); nil for a single
	// event. Moving an occurrence changes Start but not OriginalStart. For an all-day occurrence
	// the adapter passes midnight of the occurrence's own date in the event's time zone (or UTC
	// midnight of that date); Key takes the wall-clock date of the value as given.
	OriginalStart *time.Time

	// Schedule group: a copy older than the stored LastModified never changes it.
	Start, End time.Time
	// AllDay events carry dates, never instants: StartDate and EndDate are YYYY-MM-DD, and EndDate
	// is exclusive (the iCalendar DTEND convention). An empty or non-later EndDate means one day.
	AllDay                 bool
	StartDate, EndDate     string
	TimeZone               string
	TimeZoneIANA           string
	UTCOffset              string
	Subject                string
	Organizer              string
	OrganizerAddress       string
	IsOrganizer, IsPrivate bool
	Cancelled              bool
	Response, ShowAs       string
	IsOnlineMeeting        bool
	Location               string
	LastModified           *time.Time

	// Detail group: sources fetch these only for events the user opened, so a later copy often
	// lacks them. Capture compares them against DetailAsOf, not LastModified.
	OnlineMeetingURL   string
	ShortJoinURL       string
	DialInConferenceID string
	DialInTollNumber   string
	TeamsThreadID      string
	AttendeesJSON      string
	LocationsJSON      string
	BodyHTML           string
	BodyText           string
	BodyType           string
	BodyPreview        string
	AttachmentsJSON    string
	HasAttachments     bool
	CategoriesJSON     string
	RecurrenceJSON     string
	ReminderMinutes    *int
	// DetailRawJSON is the whole scrubbed record of the copy that last supplied detail.
	DetailRawJSON string

	// Bookkeeping. DetailAsOf is the LastModified of the copy that supplied the current detail; nil
	// when detail was never captured.
	DetailAsOf   *time.Time
	DetailSeenAt *time.Time
	FirstSeenAt  time.Time
	SeenAt       time.Time
	RemovedAt    *time.Time
}

// Window is the range one source covered in a snapshot, and how fresh that snapshot was.
type Window struct {
	Source       Source
	AccountID    string
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
