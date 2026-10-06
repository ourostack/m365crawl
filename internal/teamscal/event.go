package teamscal

import (
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// AccountID is the calendar partition key for a Teams account, "<tenantId>/<userId>", the form
// --account uses.
func AccountID(acct teamsdesktop.Account) string { return acct.TenantID + "/" + acct.UserID }

// MapEventRecord maps one record of the calendar store to a core event. key is the record key
// (the object id) and becomes SourceID even when the record's own objectId differs, because
// removal matches records.key_json. zone is the zone the sync groups days in; it is used only to
// recognize an all-day event whose own zone is unknown. The record is the already-scrubbed
// canonical JSON from the archive, and the mapper keeps it whole as DetailRawJSON.
func MapEventRecord(acct teamsdesktop.Account, key string, valueJSON []byte, zone *time.Location) (calendar.Event, MapNotes, error) {
	var notes MapNotes
	if key == "" {
		return calendar.Event{}, notes, &UnmappedError{Reason: "no record key"}
	}
	m, err := decode(valueJSON)
	if err != nil {
		return calendar.Event{}, notes, err
	}
	uid := str(m["iCalUID"])
	notes.MissingICalUID = uid == ""
	start, err := requiredTime(m, "startTime")
	if err != nil {
		return calendar.Event{}, notes, err
	}
	end, err := requiredTime(m, "endTime")
	if err != nil {
		return calendar.Event{}, notes, err
	}
	if end.Before(start) {
		return calendar.Event{}, notes, &UnmappedError{Reason: "endTime is before startTime"}
	}
	e := calendar.Event{
		Source:           calendar.SourceTeams,
		AccountID:        AccountID(acct),
		SourceID:         key,
		GlobalID:         uid,
		ICalUID:          uid,
		SeriesKey:        str(m["cleanGlobalObjectId"]),
		Start:            start,
		End:              end,
		TimeZone:         str(m["eventTimeZone"]),
		UTCOffset:        str(m["utcOffset"]),
		Subject:          str(m["subject"]),
		Organizer:        str(m["organizerName"]),
		OrganizerAddress: str(m["organizerAddress"]),
		IsOrganizer:      triFlag(m, "isOrganizer"),
		IsPrivate:        triFlag(m, "isPrivate"),
		Cancelled:        triFlag(m, "isCancelled"),
		Response:         normalizeResponse(str(m["myResponseType"])),
		ShowAs:           strings.ToLower(str(m["showAs"])),
		IsOnlineMeeting:  triFlag(m, "isOnlineMeeting"),
		Location:         str(m["location"]),
		LastModified:     timePtr(moment(m["lastModifiedTime"])),

		OnlineMeetingURL:   str(m["skypeTeamsMeetingUrl"]),
		ShortJoinURL:       str(m["shortOnlineMeetingJoinUrl"]),
		DialInConferenceID: str(m["onlineMeetingConferenceId"]),
		DialInTollNumber:   str(m["onlineMeetingTollNumber"]),
		AttendeesJSON:      mapAttendees(m["attendees"]),
		LocationsJSON:      mapLocations(m["meetingLocations"]),
		BodyPreview:        str(m["bodyPreview"]),
		AttachmentsJSON:    mapAttachments(m["attachments"]),
		HasAttachments:     triFlag(m, "hasAttachments"),
		CategoriesJSON:     mapCategories(m),
		RecurrenceJSON:     mapRecurrence(m),
		DetailRawJSON:      string(valueJSON),
	}
	e.EventType, notes.EventTypeAbsent = normalizeEventType(str(m["eventType"]))
	var reminderKnown bool
	e.ReminderMinutes, reminderKnown, notes.ReminderOutOfRange = mapReminder(m)

	e.TeamsThreadID = threadID(m, e.OnlineMeetingURL)

	if bodyHTML := str(m["bodyContent"]); bodyHTML != "" {
		e.BodyHTML, e.BodyType = bodyHTML, str(m["bodyContentType"])
		e.BodyText = bodyHTML
		if !strings.EqualFold(e.BodyType, "text") {
			e.BodyText = teamsdesktop.HTMLToText(bodyHTML)
		}
	}

	var eventLoc *time.Location
	if e.TimeZone != "" {
		var ok bool
		e.TimeZoneIANA, eventLoc, ok = resolveZone(e.TimeZone)
		if !ok {
			notes.UnknownZone = e.TimeZone
		}
	}

	// A missing isAllDayEvent is unknown, not false: 7 of 148 measured records lack the key.
	e.AllDay = triFlag(m, "isAllDayEvent")
	if e.AllDay.Is(true) {
		if start, end, ok := allDayDates(e.Start, e.End, eventLoc, zone); ok {
			e.StartDate, e.EndDate = start, end
		} else {
			e.AllDay = calendar.TriFalse // stored as timed, as the note says
			notes.AllDayUnaligned = true
		}
	}
	e.Unknown = unknownFields(m, reminderKnown)
	e.UnknownDeclared = true // every vocabulary field is stated or listed above
	if err := calendar.ValidateEvent(e); err != nil {
		return calendar.Event{}, notes, &UnmappedError{Reason: err.Error()}
	}
	return e, notes, nil
}

// triFlag is the three-valued flag key of m: known when the key holds a boolean, unknown (the zero
// value) when it is absent or not a boolean. Absence must stay absence: it is not false.
func triFlag(m map[string]any, key string) calendar.Tri {
	if v, ok := flag(m, key); ok {
		return calendar.TriOf(v)
	}
	return calendar.TriUnknown
}

// has reports whether any of the record's keys holds a value (a key that is absent or null does
// not).
func has(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if m[k] != nil {
			return true
		}
	}
	return false
}

// unknownFields names the non-flag fields whose record keys are absent: a present key, even an
// empty one, is known; an absent key is not known. A thin record therefore leaves the whole detail
// group unknown. The reminder is known when isReminderSet was read and usable.
func unknownFields(m map[string]any, reminderKnown bool) []calendar.Field {
	var out []calendar.Field
	for _, f := range []struct {
		name calendar.Field
		keys []string
	}{
		{calendar.FieldSubject, []string{"subject"}},
		{calendar.FieldLocation, []string{"location"}},
		{calendar.FieldOrganizer, []string{"organizerName"}},
		{calendar.FieldOrganizerAddress, []string{"organizerAddress"}},
		{calendar.FieldResponse, []string{"myResponseType"}},
		{calendar.FieldShowAs, []string{"showAs"}},
		{calendar.FieldTimeZone, []string{"eventTimeZone"}},
		{calendar.FieldTimeZoneIANA, []string{"eventTimeZone"}}, // derived from the zone name
		{calendar.FieldUTCOffset, []string{"utcOffset"}},
		{calendar.FieldJoinURL, []string{"skypeTeamsMeetingUrl"}},
		{calendar.FieldShortJoinURL, []string{"shortOnlineMeetingJoinUrl"}},
		{calendar.FieldDialIn, []string{"onlineMeetingConferenceId", "onlineMeetingTollNumber"}},
		{calendar.FieldMeetingChatID, []string{"skypeTeamsDataObject", "skypeTeamsData", "skypeTeamsMeetingUrl"}},
		{calendar.FieldAttendees, []string{"attendees"}},
		{calendar.FieldRooms, []string{"meetingLocations"}},
		{calendar.FieldBody, []string{"bodyContent"}},
		{calendar.FieldBodyPreview, []string{"bodyPreview"}},
		{calendar.FieldAttachments, []string{"attachments"}},
		{calendar.FieldCategories, []string{"categories"}},
		{calendar.FieldRecurrence, recurrenceFields},
	} {
		if !has(m, f.keys...) {
			out = append(out, f.name)
		}
	}
	if !reminderKnown {
		out = append(out, calendar.FieldReminder)
	}
	return calendar.NormalizeUnknown(out)
}

// normalizeEventType maps Teams' eventType to the core's lower-case vocabulary. An absent type is
// a single event, reported so the caller can count it. An unknown type is kept lower-cased.
func normalizeEventType(s string) (typ string, absent bool) {
	switch l := strings.ToLower(strings.TrimSpace(s)); l {
	case "":
		return calendar.EventSingle, true
	case "recurringmaster":
		return calendar.EventMaster, false
	default:
		return l, false
	}
}

// normalizeResponse maps a response type to the shared vocabulary: accepted, tentative,
// declined, none (not responded), organizer. An unknown value is kept lower-cased.
func normalizeResponse(s string) string {
	switch l := strings.ToLower(strings.TrimSpace(s)); l {
	case "notresponded":
		return "none"
	case "tentativelyaccepted":
		return "tentative"
	default:
		return l
	}
}

// threadID is the meeting chat's thread id: skypeTeamsDataObject.cid, then the same field inside
// skypeTeamsData, else the id parsed out of the join link.
func threadID(m map[string]any, joinURL string) string {
	for _, k := range []string{"skypeTeamsDataObject", "skypeTeamsData"} {
		if cid := str(object(m[k])["cid"]); cid != "" {
			return cid
		}
	}
	return calendar.ParseTeamsThreadID(joinURL)
}

type attendee struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Role     string `json:"role"`
	Response string `json:"response"`
}

// mapAttendees stores each attendee as {name, address, type, role, response}, the response taken
// from status.response (or a plain status string) and normalized. Entries with neither name nor
// address are dropped. The result is "" when nothing is left.
func mapAttendees(v any) string {
	var out []attendee
	for _, it := range array(v) {
		m := object(it)
		a := attendee{Name: str(m["name"]), Address: str(m["address"]), Type: str(m["type"]), Role: str(m["role"])}
		if a.Name == "" && a.Address == "" {
			continue
		}
		status := m["status"]
		if o := object(status); o != nil {
			status = o["response"]
		}
		a.Response = normalizeResponse(str(status))
		out = append(out, a)
	}
	return listJSON(out)
}

type location struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// mapLocations stores meetingLocations as {name, kind, address, latitude, longitude}. The
// address may be text or an object of address parts.
func mapLocations(v any) string {
	var out []location
	for _, it := range array(v) {
		m := object(it)
		l := location{Name: str(m["displayName"]), Kind: str(m["locationType"]), Address: addressText(m["address"])}
		if l.Name == "" && l.Address == "" {
			continue
		}
		if c := object(m["coordinates"]); c != nil {
			if lat, ok := number(c["latitude"]); ok {
				if lon, ok := number(c["longitude"]); ok {
					l.Latitude, l.Longitude = &lat, &lon
				}
			}
		}
		out = append(out, l)
	}
	return listJSON(out)
}

// addressText reads an address given as text or as an object of parts.
func addressText(v any) string {
	o := object(v)
	if o == nil {
		return str(v)
	}
	var parts []string
	for _, k := range []string{"street", "city", "state", "postalCode", "countryOrRegion"} {
		if s := str(o[k]); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

type attachment struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	ContentType    string `json:"content_type"`
	ContentID      string `json:"content_id"`
	IsInline       bool   `json:"is_inline"`
	AttachmentType string `json:"attachment_type"`
}

// mapAttachments stores attachment metadata as {id, name, size, content_type, content_id,
// is_inline, attachment_type}; the cache carries no url, and files are never downloaded. The name
// is name, else fileName.
func mapAttachments(v any) string {
	var out []attachment
	for _, it := range array(v) {
		m := object(it)
		a := attachment{
			ID: str(m["id"]), Name: firstNonEmpty(str(m["name"]), str(m["fileName"])),
			ContentType: str(m["contentType"]), ContentID: str(m["contentId"]),
			IsInline: boolean(m["isInline"]), AttachmentType: str(m["attachmentType"]),
		}
		a.Size, _ = integer(m["size"])
		if a.ID == "" && a.Name == "" {
			continue
		}
		out = append(out, a)
	}
	return listJSON(out)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// maxReminderMinutes is four weeks; a lead time outside 0..maxReminderMinutes is not believed.
const maxReminderMinutes = 40320

// mapReminder reads the reminder from isReminderSet: true gives the lead time (zero minutes is a
// value), false states that no reminder is set (nil minutes, known), and an absent or non-boolean
// key is not known. A lead time that is negative or above four weeks is not stated, and
// outOfRange says so.
func mapReminder(m map[string]any) (minutes *int, known, outOfRange bool) {
	set, present := flag(m, "isReminderSet")
	if !present {
		return nil, false, false
	}
	if !set {
		return nil, true, false
	}
	n, ok := integer(m["reminderMinutesBeforeStart"])
	if !ok {
		return nil, false, false
	}
	if n < 0 || n > maxReminderMinutes {
		return nil, false, true
	}
	v := int(n)
	return &v, true, false
}

// mapCategories renders the category list. "[]" states "no categories" and is emitted only when
// the categories key holds a list with no names; an absent, null or unreadable value gives "", which states
// nothing.
func mapCategories(m map[string]any) string {
	list, ok := jsonish(m["categories"]).([]any)
	if !ok {
		return ""
	}
	out := []string{}
	for _, it := range list {
		if s := str(it); s != "" {
			out = append(out, s)
		}
	}
	return marshal(out)
}

// recurrenceFields are the record fields that together describe a series' rule.
var recurrenceFields = []string{"recurrencePattern", "eventRecurrencePattern", "eventRecurrenceRange", "recurrenceEnd"}

// mapRecurrence gathers the rule fields that are present into one object, "" when none is.
func mapRecurrence(m map[string]any) string {
	rule := map[string]any{}
	for _, k := range recurrenceFields {
		if v, ok := m[k]; ok && v != nil {
			rule[k] = jsonish(v)
		}
	}
	if len(rule) == 0 {
		return ""
	}
	return marshal(rule)
}

// listJSON renders a list as JSON, or "" for an empty one so that "no detail" stays empty.
func listJSON[T any](list []T) string {
	if len(list) == 0 {
		return ""
	}
	return marshal(list)
}
