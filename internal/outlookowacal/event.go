// Package outlookowacal maps the Windows Outlook event cache to the shared calendar.
package outlookowacal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
)

// Notes contains counts only, never values from a source event.
type Notes struct {
	Unmapped, UnknownZone, AllDayUnaligned, Redacted, PartialAttendees int
}

// MapEvent leaves unobserved fields unknown and returns only fixed errors.
func MapEvent(account string, value any) (calendar.Event, Notes, error) {
	r := eventReader{}
	o, ok := validObject(value)
	if !ok || strings.TrimSpace(account) == "" {
		return calendar.Event{}, r.notes, errors.New("outlook_owa_calendar_unmapped")
	}
	uid, ok := r.text(o, "UID")
	if !ok || uid == "" {
		return calendar.Event{}, r.notes, errors.New("outlook_owa_calendar_identity")
	}
	if _, err := hex.DecodeString(uid); err != nil {
		return calendar.Event{}, r.notes, errors.New("outlook_owa_calendar_identity")
	}
	start, okS := r.instant(o, "Start")
	end, okE := r.instant(o, "End")
	modified, okM := r.instant(o, "LastModifiedTime")
	if !okS || !okE || !okM || end.Before(start) {
		return calendar.Event{}, r.notes, errors.New("outlook_owa_calendar_time")
	}
	uid = strings.ToLower(uid)
	e := calendar.Event{
		Source: calendar.SourceOutlook, AccountID: account,
		SourceID: uid, GlobalID: uid, ICalUID: uid, SeriesKey: uid,
		Start: start, End: end, LastModified: &modified,
		AllDay: r.flag(o, "IsAllDayEvent"), Cancelled: r.flag(o, "IsCancelled"),
		IsOnlineMeeting: r.flag(o, "IsOnlineMeeting"), IsOrganizer: r.flag(o, "IsOrganizer"),
		HasAttachments: r.flag(o, "HasAttachments"),
	}
	if series, _, ok := calendar.SplitOccurrenceID(uid); ok {
		e.SeriesKey = strings.ToLower(series)
	}
	known := map[calendar.Field]bool{}
	assign := func(key string, field calendar.Field, target *string) {
		if s, ok := r.text(o, key); ok {
			*target, known[field] = s, true
		}
	}
	assign("Subject", calendar.FieldSubject, &e.Subject)
	assign("Location", calendar.FieldLocation, &e.Location)
	assign("OnlineMeetingJoinUrl", calendar.FieldJoinURL, &e.OnlineMeetingURL)
	if thread := calendar.ParseTeamsThreadID(e.OnlineMeetingURL); thread != "" {
		e.TeamsThreadID, known[calendar.FieldMeetingChatID] = thread, true
	}
	if org, present := valueOf(o, "Organizer"); present {
		if outer, ok := validObject(org); ok {
			if mb, present := valueOf(outer, "Mailbox"); present {
				if mailbox, ok := validObject(mb); ok {
					e.Organizer, known[calendar.FieldOrganizer] = r.text(mailbox, "Name")
					e.OrganizerAddress, known[calendar.FieldOrganizerAddress] = r.text(mailbox, "EmailAddress")
				} else {
					r.notes.Unmapped++
				}
			}
		} else {
			r.notes.Unmapped++
		}
	}
	var location *time.Location
	if zone, ok := r.text(o, "StartTimeZoneId"); ok {
		e.TimeZone, known[calendar.FieldTimeZone] = zone, true
		if iana, loc, found := outlookcal.ZoneIANA(zone); found {
			e.TimeZoneIANA, known[calendar.FieldTimeZoneIANA] = iana, true
			location = loc
		} else {
			r.notes.UnknownZone++
		}
	}
	if typ, ok := r.text(o, "CalendarItemType"); ok {
		switch typ {
		case "Single":
			e.EventType = calendar.EventSingle
		case "Occurrence":
			e.EventType = calendar.EventOccurrence
		case "Exception":
			e.EventType = calendar.EventException
		case "RecurringMaster":
			e.EventType = calendar.EventMaster
		default:
			r.notes.Unmapped++
		}
	}
	e.Response, known[calendar.FieldResponse] = r.response(o)
	r.details(o, &e, known)
	if e.AllDay.Is(true) {
		if first, last, ok := r.allDayDates(o, start, end, e.TimeZoneIANA, location); ok {
			e.StartDate, e.EndDate = first, last
		} else {
			e.AllDay = calendar.TriUnknown
			r.notes.AllDayUnaligned++
		}
	}
	for _, field := range []calendar.Field{
		calendar.FieldSubject, calendar.FieldLocation, calendar.FieldOrganizer, calendar.FieldOrganizerAddress,
		calendar.FieldResponse, calendar.FieldShowAs, calendar.FieldTimeZone, calendar.FieldTimeZoneIANA,
		calendar.FieldUTCOffset, calendar.FieldJoinURL, calendar.FieldShortJoinURL, calendar.FieldDialIn,
		calendar.FieldMeetingChatID, calendar.FieldAttendees, calendar.FieldRooms, calendar.FieldBody,
		calendar.FieldBodyPreview, calendar.FieldAttachments, calendar.FieldCategories, calendar.FieldRecurrence, calendar.FieldReminder,
	} {
		if !known[field] {
			e.Unknown = append(e.Unknown, field)
		}
	}
	e.Unknown, e.UnknownDeclared = calendar.NormalizeUnknown(e.Unknown), true
	return e, r.notes, nil
}

type eventReader struct{ notes Notes }

func (r *eventReader) allDayDates(o *v8.Object, start, end time.Time, iana string, location *time.Location) (string, string, bool) {
	if location == nil {
		return "", "", false
	}
	if _, present := valueOf(o, "EndTimeZoneId"); present {
		zone, ok := r.text(o, "EndTimeZoneId")
		if !ok {
			return "", "", false
		}
		endIANA, _, found := outlookcal.ZoneIANA(zone)
		if !found {
			r.notes.UnknownZone++
			return "", "", false
		}
		if endIANA != iana {
			return "", "", false
		}
	}
	first, last := start.In(location), end.In(location)
	for _, instant := range []time.Time{first, last} {
		if instant.Year() < 1 || instant.Year() > 9999 ||
			instant.Hour() != 0 || instant.Minute() != 0 || instant.Second() != 0 || instant.Nanosecond() != 0 {
			return "", "", false
		}
	}
	firstDate, lastDate := first.Format(time.DateOnly), last.Format(time.DateOnly)
	if lastDate <= firstDate {
		return "", "", false
	}
	return firstDate, lastDate, true
}

func validObject(value any) (*v8.Object, bool) {
	o, ok := value.(*v8.Object)
	if !ok || o == nil || len(o.Keys) != len(o.Values) {
		return nil, false
	}
	seen := map[string]bool{}
	for _, key := range o.Keys {
		if seen[key] {
			return nil, false
		}
		seen[key] = true
	}
	return o, true
}

func valueOf(o *v8.Object, key string) (any, bool) {
	for i, name := range o.Keys {
		if name == key {
			v := o.Values[i]
			_, undefined := v.(v8.Undefined)
			return v, v != nil && !undefined
		}
	}
	return nil, false
}

func (r *eventReader) text(o *v8.Object, key string) (string, bool) {
	value, present := valueOf(o, key)
	if !present {
		return "", false
	}
	s, ok := value.(string)
	if ok && key == "Preview" && !utf8.ValidString(s) {
		s, ok = previewUnicode(s)
	}
	if !ok || !utf8.ValidString(s) {
		r.notes.Unmapped++
		return "", false
	}
	if teamsdesktop.MayRedact(s) {
		var data bytes.Buffer
		encoder := json.NewEncoder(&data)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(s) // A valid string always encodes to a bytes.Buffer.
		clean, count := teamsdesktop.Scrub(data.Bytes())
		r.notes.Redacted += count
		_ = json.Unmarshal(clean, &s) // Scrubbing a JSON string preserves its type.
	}
	return s, true
}

func (r *eventReader) instant(o *v8.Object, key string) (time.Time, bool) {
	s, ok := r.text(o, key)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	t = t.UTC()
	return t, err == nil && !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999
}

func (r *eventReader) flag(o *v8.Object, key string) calendar.Tri {
	value, present := valueOf(o, key)
	if !present {
		return calendar.TriUnknown
	}
	if b, ok := value.(bool); ok {
		return calendar.TriOf(b)
	}
	r.notes.Unmapped++
	return calendar.TriUnknown
}
