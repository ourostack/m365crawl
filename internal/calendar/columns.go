package calendar

import (
	"database/sql"
	"strings"
)

// scanEvent is the scan target of one calendar_source_events row: the Event plus the temporaries
// its text-typed columns need.
type scanEvent struct {
	Event
	src                                                        string
	orig, start, end, mod, detailAsOf, detailSeen, first, seen timeText
	removed                                                    timeText
	remind                                                     sql.NullInt64
}

// finish moves the scanned temporaries into the Event.
func (s *scanEvent) finish() Event {
	e := s.Event
	e.Source = Source(s.src)
	e.OriginalStart, e.Start, e.End, e.LastModified = s.orig.ptr(), s.start.t, s.end.t, s.mod.ptr()
	e.DetailAsOf, e.DetailSeenAt, e.RemovedAt = s.detailAsOf.ptr(), s.detailSeen.ptr(), s.removed.ptr()
	e.FirstSeenAt, e.SeenAt = s.first.t, s.seen.t
	if s.remind.Valid {
		n := int(s.remind.Int64)
		e.ReminderMinutes = &n
	}
	return e
}

// column binds one calendar_source_events column to the Event field it holds: val renders the
// field for an INSERT, dst returns the scan target. heavy columns are left out of agenda reads.
type column struct {
	name  string
	val   func(e Event) any
	dst   func(s *scanEvent) any
	heavy bool
}

// eventColumns is the single list of calendar_source_events columns in insert order, apart from
// event_key and composite_key, which the store computes. The primary key is source, account_id,
// event_key.
var eventColumns = []column{
	{name: "source", val: func(e Event) any { return string(e.Source) }, dst: func(s *scanEvent) any { return &s.src }},
	{name: "account_id", val: func(e Event) any { return e.AccountID }, dst: func(s *scanEvent) any { return &s.AccountID }},
	{name: "source_id", val: func(e Event) any { return e.SourceID }, dst: func(s *scanEvent) any { return &s.SourceID }},
	{name: "global_id", val: func(e Event) any { return e.GlobalID }, dst: func(s *scanEvent) any { return &s.GlobalID }},
	{name: "ical_uid", val: func(e Event) any { return e.ICalUID }, dst: func(s *scanEvent) any { return &s.ICalUID }},
	{name: "series_key", val: func(e Event) any { return e.SeriesKey }, dst: func(s *scanEvent) any { return &s.SeriesKey }},
	{name: "event_type", val: func(e Event) any { return e.EventType }, dst: func(s *scanEvent) any { return &s.EventType }},
	{name: "original_start", val: func(e Event) any { return formatTimePtr(e.OriginalStart) }, dst: func(s *scanEvent) any { return &s.orig }},
	{name: "start_at", val: func(e Event) any { return formatTime(e.Start) }, dst: func(s *scanEvent) any { return &s.start }},
	{name: "end_at", val: func(e Event) any { return formatTime(e.End) }, dst: func(s *scanEvent) any { return &s.end }},
	{name: "all_day", val: func(e Event) any { return e.AllDay }, dst: func(s *scanEvent) any { return &s.AllDay }},
	{name: "start_date", val: func(e Event) any { return e.StartDate }, dst: func(s *scanEvent) any { return &s.StartDate }},
	{name: "end_date", val: func(e Event) any { return e.EndDate }, dst: func(s *scanEvent) any { return &s.EndDate }},
	{name: "time_zone", val: func(e Event) any { return e.TimeZone }, dst: func(s *scanEvent) any { return &s.TimeZone }},
	{name: "time_zone_iana", val: func(e Event) any { return e.TimeZoneIANA }, dst: func(s *scanEvent) any { return &s.TimeZoneIANA }},
	{name: "utc_offset", val: func(e Event) any { return e.UTCOffset }, dst: func(s *scanEvent) any { return &s.UTCOffset }},
	{name: "subject", val: func(e Event) any { return e.Subject }, dst: func(s *scanEvent) any { return &s.Subject }},
	{name: "organizer", val: func(e Event) any { return e.Organizer }, dst: func(s *scanEvent) any { return &s.Organizer }},
	{name: "organizer_address", val: func(e Event) any { return e.OrganizerAddress }, dst: func(s *scanEvent) any { return &s.OrganizerAddress }},
	{name: "is_organizer", val: func(e Event) any { return e.IsOrganizer }, dst: func(s *scanEvent) any { return &s.IsOrganizer }},
	{name: "is_private", val: func(e Event) any { return e.IsPrivate }, dst: func(s *scanEvent) any { return &s.IsPrivate }},
	{name: "cancelled", val: func(e Event) any { return e.Cancelled }, dst: func(s *scanEvent) any { return &s.Cancelled }},
	{name: "response", val: func(e Event) any { return e.Response }, dst: func(s *scanEvent) any { return &s.Response }},
	{name: "show_as", val: func(e Event) any { return e.ShowAs }, dst: func(s *scanEvent) any { return &s.ShowAs }},
	{name: "is_online_meeting", val: func(e Event) any { return e.IsOnlineMeeting }, dst: func(s *scanEvent) any { return &s.IsOnlineMeeting }},
	{name: "location", val: func(e Event) any { return e.Location }, dst: func(s *scanEvent) any { return &s.Location }},
	{name: "last_modified", val: func(e Event) any { return formatTimePtr(e.LastModified) }, dst: func(s *scanEvent) any { return &s.mod }},
	{name: "online_meeting_url", val: func(e Event) any { return e.OnlineMeetingURL }, dst: func(s *scanEvent) any { return &s.OnlineMeetingURL }},
	{name: "short_join_url", val: func(e Event) any { return e.ShortJoinURL }, dst: func(s *scanEvent) any { return &s.ShortJoinURL }},
	{name: "dial_in_conference_id", val: func(e Event) any { return e.DialInConferenceID }, dst: func(s *scanEvent) any { return &s.DialInConferenceID }},
	{name: "dial_in_toll_number", val: func(e Event) any { return e.DialInTollNumber }, dst: func(s *scanEvent) any { return &s.DialInTollNumber }},
	{name: "teams_thread_id", val: func(e Event) any { return e.TeamsThreadID }, dst: func(s *scanEvent) any { return &s.TeamsThreadID }},
	{name: "attendees_json", val: func(e Event) any { return e.AttendeesJSON }, dst: func(s *scanEvent) any { return &s.AttendeesJSON }},
	{name: "locations_json", val: func(e Event) any { return e.LocationsJSON }, dst: func(s *scanEvent) any { return &s.LocationsJSON }},
	{name: "body_html", heavy: true, val: func(e Event) any { return e.BodyHTML }, dst: func(s *scanEvent) any { return &s.BodyHTML }},
	{name: "body_text", heavy: true, val: func(e Event) any { return e.BodyText }, dst: func(s *scanEvent) any { return &s.BodyText }},
	{name: "body_type", val: func(e Event) any { return e.BodyType }, dst: func(s *scanEvent) any { return &s.BodyType }},
	{name: "body_preview", val: func(e Event) any { return e.BodyPreview }, dst: func(s *scanEvent) any { return &s.BodyPreview }},
	{name: "attachments_json", val: func(e Event) any { return e.AttachmentsJSON }, dst: func(s *scanEvent) any { return &s.AttachmentsJSON }},
	{name: "has_attachments", val: func(e Event) any { return e.HasAttachments }, dst: func(s *scanEvent) any { return &s.HasAttachments }},
	{name: "categories_json", val: func(e Event) any { return e.CategoriesJSON }, dst: func(s *scanEvent) any { return &s.CategoriesJSON }},
	{name: "recurrence_json", val: func(e Event) any { return e.RecurrenceJSON }, dst: func(s *scanEvent) any { return &s.RecurrenceJSON }},
	{name: "reminder_minutes", val: func(e Event) any {
		if e.ReminderMinutes == nil {
			return nil
		}
		return *e.ReminderMinutes
	}, dst: func(s *scanEvent) any { return &s.remind }},
	{name: "detail_raw_json", heavy: true, val: func(e Event) any { return e.DetailRawJSON }, dst: func(s *scanEvent) any { return &s.DetailRawJSON }},
	{name: "detail_as_of", val: func(e Event) any { return formatTimePtr(e.DetailAsOf) }, dst: func(s *scanEvent) any { return &s.detailAsOf }},
	{name: "detail_seen_at", val: func(e Event) any { return formatTimePtr(e.DetailSeenAt) }, dst: func(s *scanEvent) any { return &s.detailSeen }},
	{name: "first_seen_at", val: func(e Event) any { return formatTime(e.FirstSeenAt) }, dst: func(s *scanEvent) any { return &s.first }},
	{name: "seen_at", val: func(e Event) any { return formatTime(e.SeenAt) }, dst: func(s *scanEvent) any { return &s.seen }},
	{name: "removed_at", val: func(e Event) any { return formatTimePtr(e.RemovedAt) }, dst: func(s *scanEvent) any { return &s.removed }},
}

// selectColumns lists the columns to read, always with event_key first; heavy columns only when
// all is true.
func selectColumns(all bool) []column {
	var out []column
	for _, c := range eventColumns {
		if all || !c.heavy {
			out = append(out, c)
		}
	}
	return out
}

// keyedEvent is a stored event and the key it is stored under.
type keyedEvent struct {
	Event
	key string
}

// selectSQL renders the SELECT for cols, with where as the condition (without the keyword).
func selectSQL(cols []column, where string) string {
	names := make([]string, 0, len(cols)+1)
	names = append(names, "event_key")
	for _, c := range cols {
		names = append(names, c.name)
	}
	return "SELECT " + strings.Join(names, ",") + " FROM calendar_source_events WHERE " + where +
		" ORDER BY source, account_id, event_key"
}

// scanKeyed scans one row selected with selectSQL(cols, ...).
func scanKeyed(rows *sql.Rows, cols []column) (keyedEvent, error) {
	var s scanEvent
	var key string
	dst := []any{&key}
	for _, c := range cols {
		dst = append(dst, c.dst(&s))
	}
	if err := scanRow(rows, dst...); err != nil {
		return keyedEvent{}, err
	}
	return keyedEvent{Event: s.finish(), key: key}, nil
}

// storedEqual reports whether two events would store the same row (bookkeeping included).
func storedEqual(a, b Event) bool {
	for _, c := range eventColumns {
		if c.val(a) != c.val(b) {
			return false
		}
	}
	return true
}
