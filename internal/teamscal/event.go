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
	e := calendar.Event{
		Source:           calendar.SourceTeams,
		AccountID:        AccountID(acct),
		SourceID:         key,
		GlobalID:         uid,
		ICalUID:          uid,
		SeriesKey:        str(m["cleanGlobalObjectId"]),
		Start:            moment(m["startTime"]),
		End:              moment(m["endTime"]),
		TimeZone:         str(m["eventTimeZone"]),
		UTCOffset:        str(m["utcOffset"]),
		Subject:          str(m["subject"]),
		Organizer:        str(m["organizerName"]),
		OrganizerAddress: str(m["organizerAddress"]),
		IsOrganizer:      flagValue(m, "isOrganizer"),
		IsPrivate:        flagValue(m, "isPrivate"),
		Cancelled:        flagValue(m, "isCancelled"),
		Response:         normalizeResponse(str(m["myResponseType"])),
		ShowAs:           strings.ToLower(str(m["showAs"])),
		IsOnlineMeeting:  flagValue(m, "isOnlineMeeting"),
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
		HasAttachments:     flagValue(m, "hasAttachments"),
		CategoriesJSON:     mapCategories(m["categories"]),
		RecurrenceJSON:     mapRecurrence(m),
		DetailRawJSON:      string(valueJSON),
	}
	e.EventType, notes.EventTypeAbsent = normalizeEventType(str(m["eventType"]))
	if minutes, ok := integer(m["reminderMinutesBeforeStart"]); ok {
		n := int(minutes)
		e.ReminderMinutes = &n
	}

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

	if flagValue(m, "isAllDayEvent") {
		if start, end, ok := allDayDates(e.Start, e.End, eventLoc, zone); ok {
			e.AllDay, e.StartDate, e.EndDate = true, start, end
		} else {
			notes.AllDayUnaligned = true
		}
	}
	return e, notes, nil
}

// flagValue is flag without the presence bit, for fields the core still holds as plain booleans.
func flagValue(m map[string]any, key string) bool {
	v, _ := flag(m, key)
	return v
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
	Name        string `json:"name"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// mapAttachments stores attachment metadata as {name, url, size, content_type}; files are never
// downloaded. The source field names are assumed (only the count of events with attachments is
// observed), so a few common spellings are accepted.
func mapAttachments(v any) string {
	var out []attachment
	for _, it := range array(v) {
		m := object(it)
		a := attachment{
			Name:        firstNonEmpty(str(m["name"]), str(m["fileName"])),
			URL:         firstNonEmpty(str(m["url"]), str(m["contentUrl"])),
			ContentType: str(m["contentType"]),
		}
		a.Size, _ = integer(m["size"])
		if a.Name == "" && a.URL == "" {
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

func mapCategories(v any) string {
	var out []string
	for _, it := range array(v) {
		if s := str(it); s != "" {
			out = append(out, s)
		}
	}
	return listJSON(out)
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
