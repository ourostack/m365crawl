package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/store"
)

// rangeFix is the usage fix for an unsigned duration given as a calendar bound.
const rangeFix = "calendar offsets are signed: +7d is a week ahead, -7d a week back."

var signedOffset = regexp.MustCompile(`^([+-])(\d+(?:\.\d+)?)([dwhm])$`)

// parseRange reads a --from or --to value: today, yesterday, tomorrow, YYYY-MM-DD (midnight in
// loc), RFC3339, or a signed offset from now (+3d, -1d, +2w, +90m, +2h). An unsigned duration such
// as 7d is a usage error, because --since reads it as "back" and accepting it here would mislead.
func parseRange(flag, s string, now time.Time, loc *time.Location) (time.Time, error) {
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch s {
	case "today":
		return midnight, nil
	case "yesterday":
		return midnight.AddDate(0, 0, -1), nil
	case "tomorrow":
		return midnight.AddDate(0, 0, 1), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	if m := signedOffset.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(m[2], 64)
		if err == nil {
			unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[3]]
			d := time.Duration(n * float64(unit))
			if m[1] == "-" {
				d = -d
			}
			return now.Add(d).In(loc), nil
		}
	}
	c := errs.Usage(fmt.Sprintf("%s: cannot read %q as a calendar time", flag, s))
	c.Fix = "Use today, yesterday, tomorrow, YYYY-MM-DD, RFC3339, or a signed offset such as +3d, -1d, +2w or +90m."
	if relativeDur.MatchString(s) {
		c.Message = fmt.Sprintf("%s: %q is an unsigned duration", flag, s)
		c.Fix = rangeFix
	}
	return time.Time{}, c
}

// calendarRoom is one place an event is held.
type calendarRoom struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	LocationType string   `json:"location_type,omitempty"`
	Address      string   `json:"address,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Stale        bool     `json:"stale,omitempty"`
}

// filledField says a schedule field was taken from another source's row than the one the event is
// based on, and when that source held it.
type filledField struct {
	Field string    `json:"field"`
	From  string    `json:"from"`
	AsOf  time.Time `json:"as_of,omitzero"`
}

// calendarItem is one event of the agenda. A known flag is printed true or false; an unknown flag
// has no key and its name is in unknown_fields. An empty string that is not named in unknown_fields
// means known and empty.
type calendarItem struct {
	EventID            string         `json:"event_id"`
	EventKey           string         `json:"event_key"`
	AccountID          string         `json:"account_id"`
	TenantID           string         `json:"tenant_id,omitempty"`
	UserID             string         `json:"user_id,omitempty"`
	Sources            []string       `json:"sources"`
	ICalUID            string         `json:"ical_uid,omitempty"`
	SeriesKey          string         `json:"series_key,omitempty"`
	EventType          string         `json:"event_type,omitempty"`
	Subject            string         `json:"subject,omitempty"`
	Start              time.Time      `json:"start"`
	End                time.Time      `json:"end"`
	StartLocal         string         `json:"start_local,omitempty"`
	EndLocal           string         `json:"end_local,omitempty"`
	AllDay             *bool          `json:"all_day,omitempty"`
	StartDate          string         `json:"start_date,omitempty"`
	EndDate            string         `json:"end_date,omitempty"`
	TimeZone           string         `json:"time_zone,omitempty"`
	TimeZoneIANA       string         `json:"time_zone_iana,omitempty"`
	Status             string         `json:"status"`
	Cancelled          *bool          `json:"cancelled,omitempty"`
	Response           string         `json:"response,omitempty"`
	ShowAs             string         `json:"show_as,omitempty"`
	IsOrganizer        *bool          `json:"is_organizer,omitempty"`
	IsPrivate          *bool          `json:"is_private,omitempty"`
	OrganizerName      string         `json:"organizer_name,omitempty"`
	OrganizerAddress   string         `json:"organizer_address,omitempty"`
	IsOnlineMeeting    *bool          `json:"is_online_meeting,omitempty"`
	JoinURL            string         `json:"join_url,omitempty"`
	ShortJoinURL       string         `json:"short_join_url,omitempty"`
	DialInConferenceID string         `json:"dial_in_conference_id,omitempty"`
	DialInTollNumber   string         `json:"dial_in_toll_number,omitempty"`
	MeetingChatID      string         `json:"meeting_chat_id,omitempty"`
	Location           string         `json:"location,omitempty"`
	Rooms              []calendarRoom `json:"rooms,omitempty"`
	RoomsAsOf          time.Time      `json:"rooms_as_of,omitzero"`
	AttendeeCount      *int           `json:"attendee_count,omitempty"`
	HasAttachments     *bool          `json:"has_attachments,omitempty"`
	BodyPreview        string         `json:"body_preview,omitempty"`
	HasRecap           bool           `json:"has_recap,omitempty"`
	ActionItemCount    int            `json:"action_item_count,omitempty"`
	RecordingCount     int            `json:"recording_count,omitempty"`
	DetailLevel        string         `json:"detail_level"`
	DetailAsOf         time.Time      `json:"detail_as_of,omitzero"`
	LastModified       time.Time      `json:"last_modified,omitzero"`
	Removed            bool           `json:"removed,omitempty"`
	RemovedBy          []string       `json:"removed_by,omitempty"`
	UnknownFields      []string       `json:"unknown_fields,omitempty"`
	FilledFields       []filledField  `json:"filled_fields,omitempty"`
}

func triPtr(t calendar.Tri) *bool {
	if !t.Known() {
		return nil
	}
	b := t.Is(true)
	return &b
}

// calendarStatus is cancelled, declined or confirmed.
func calendarStatus(e calendar.Event) string {
	switch {
	case e.Cancelled.Is(true):
		return "cancelled"
	case e.Response == "declined":
		return "declined"
	}
	return "confirmed"
}

func strs[T ~string](in []T) []string {
	var out []string
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

func localTime(t time.Time, iana string) string {
	if t.IsZero() || iana == "" {
		return ""
	}
	loc, err := time.LoadLocation(iana)
	if err != nil {
		return ""
	}
	return t.In(loc).Format(time.RFC3339)
}

// itemOf renders one agenda row.
func itemOf(r store.CalendarRow) calendarItem {
	e := r.Event
	it := calendarItem{
		EventID: r.EventID, EventKey: r.Key, AccountID: r.Principal, Sources: strs(r.Sources),
		ICalUID: e.ICalUID, SeriesKey: e.SeriesKey, EventType: e.EventType, Subject: e.Subject,
		Start: e.Start.UTC(), End: e.End.UTC(), StartLocal: localTime(e.Start, e.TimeZoneIANA), EndLocal: localTime(e.End, e.TimeZoneIANA),
		AllDay: triPtr(e.AllDay), StartDate: e.StartDate, EndDate: e.EndDate, TimeZone: e.TimeZone, TimeZoneIANA: e.TimeZoneIANA,
		Status: calendarStatus(e), Cancelled: triPtr(e.Cancelled), Response: e.Response, ShowAs: e.ShowAs,
		IsOrganizer: triPtr(e.IsOrganizer), IsPrivate: triPtr(e.IsPrivate), OrganizerName: e.Organizer, OrganizerAddress: e.OrganizerAddress,
		IsOnlineMeeting: triPtr(e.IsOnlineMeeting), JoinURL: e.OnlineMeetingURL, ShortJoinURL: e.ShortJoinURL,
		DialInConferenceID: e.DialInConferenceID, DialInTollNumber: e.DialInTollNumber, MeetingChatID: e.TeamsThreadID,
		Location: e.Location, HasAttachments: triPtr(e.HasAttachments), BodyPreview: e.BodyPreview,
		HasRecap: r.HasRecap, ActionItemCount: r.ActionItems, RecordingCount: r.Recordings,
		DetailLevel: r.DetailLevel, Removed: r.Removed, RemovedBy: strs(r.RemovedBy), UnknownFields: calendar.UnknownFields(e),
	}
	if e.DetailAsOf != nil {
		it.DetailAsOf = e.DetailAsOf.UTC()
	}
	if e.LastModified != nil {
		it.LastModified = e.LastModified.UTC()
	}
	for _, s := range r.Sources {
		if s == calendar.SourceTeams {
			it.TenantID, it.UserID, _ = strings.Cut(r.Principal, "/")
		}
	}
	// A room list nobody stated is not printed: calendar.Rooms still builds text rooms from the
	// location, which location already carries, and rooms must never sit next to "rooms" in
	// unknown_fields.
	for _, rm := range r.Rooms {
		if contains(it.UnknownFields, string(calendar.FieldRooms)) {
			break
		}
		it.Rooms = append(it.Rooms, calendarRoom{Name: rm.Name, Kind: rm.Kind, LocationType: rm.LocationType, Address: rm.Address, Latitude: rm.Latitude, Longitude: rm.Longitude, Stale: rm.Stale})
		if rm.Kind != calendar.RoomText && !it.DetailAsOf.IsZero() {
			it.RoomsAsOf = it.DetailAsOf
		}
	}
	if r.DetailLevel != calendar.DetailBasic && e.AttendeesJSON != "" {
		var list []json.RawMessage
		if json.Unmarshal([]byte(e.AttendeesJSON), &list) == nil {
			n := len(list)
			it.AttendeeCount = &n
		}
	}
	for _, f := range r.Filled {
		it.FilledFields = append(it.FilledFields, filledField{Field: string(f.Field), From: string(f.Source), AsOf: asOf(f.AsOf)})
	}
	return it
}

func asOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// rangeInfo is the range an agenda covers, in the zone its bounds were read in.
type rangeInfo struct {
	From string `json:"from"`
	To   string `json:"to"`
	Zone string `json:"zone"`
}

// calendarGroup is the calendar command: with no subcommand it is the agenda.
type calendarGroup struct {
	Agenda calendarCmd      `cmd:"" default:"withargs" hidden:"" help:"The agenda for a range."`
	Event  calendarEventCmd `cmd:"" help:"One event with everything the archive holds about it: attendees, body, recaps, action items and the recordings that belong to this occurrence."`
}

type calendarCmd struct {
	From             string `help:"Start of the range: today, yesterday, tomorrow, YYYY-MM-DD (midnight in this machine's zone), RFC3339, or a signed offset from now (+3d, -1d, +2w, +90m). Default: today." placeholder:"WHEN"`
	To               string `help:"End of the range, exclusive; same forms as --from. Default: the start of the next day. Not with --days." placeholder:"WHEN"`
	Days             int    `help:"Range length in days from --from (instead of --to)."`
	Query            string `help:"Only events whose subject, organizer or location contains this text, ignoring case."`
	IncludeCancelled bool   `name:"include-cancelled" help:"Also list cancelled events."`
	IncludeDeclined  bool   `name:"include-declined" help:"Also list events you declined."`
	IncludeMasters   bool   `name:"include-masters" help:"Also list recurring masters, which stand for the whole series."`
	IncludeRemoved   bool   `name:"include-removed" help:"Also list events a source saw go (removed is true on them)."`
	Limit            int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
}

// eventOnlyKeys are the keys only calendar event has; asking calendar for one says where it is.
var eventOnlyKeys = []string{"organizer", "attendees", "attendees_as_of", "response_counts", "body_text", "body_html", "body_type",
	"attachments", "categories", "reminder_minutes", "series", "recaps", "chat", "recordings", "series_recordings", "series_recordings_total"}

func checkCalendarFields(rt *runtime) error {
	for _, f := range rt.fields {
		if contains(eventOnlyKeys, f) {
			c := errs.Usage(fmt.Sprintf("--fields key %q is in `calendar event` only: the agenda carries no attendee list, body, recap or recording text", f))
			c.Fix = "Run `teamscrawl calendar event <event_id> --fields " + f + "`, taking the event_id from the agenda."
			return c
		}
	}
	return checkFields[calendarItem](rt)
}

func (c *calendarCmd) Run(rt *runtime) error {
	if err := checkCalendarFields(rt); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	from, to, err := c.bounds(rt)
	if err != nil {
		return err
	}
	return rt.read("calendar", func(st *store.Store) (result, error) {
		gap := true
		out := newList(nil, false)
		out.CoverageGap, out.Range = &gap, rangeOf(from, to)
		if st == nil {
			return out, nil
		}
		agenda, err := st.CalendarAgenda(rt.ctx, store.CalendarFilter{
			Account: rt.account, From: from, To: to, Query: c.Query, Limit: c.Limit,
			IncludeCancelled: c.IncludeCancelled, IncludeDeclined: c.IncludeDeclined, IncludeMasters: c.IncludeMasters, IncludeRemoved: c.IncludeRemoved,
		})
		if err != nil {
			return nil, err
		}
		if agenda.NoTables {
			out.setNeedsSync("the archive has no calendar tables yet: run teamscrawl sync")
			return out, nil
		}
		items := make([]calendarItem, len(agenda.Rows))
		for i, r := range agenda.Rows {
			items[i] = itemOf(r)
		}
		list := newList(shape(rt, items), agenda.Truncated).withTotal(agenda.Total)
		list.CoverageGap, list.Range, list.UnlinkedAccounts = &agenda.Gap, out.Range, agenda.Unlinked
		for _, u := range agenda.UnlinkedRecaps {
			list.UnlinkedRecaps = append(list.UnlinkedRecaps, unlinkedRecap{AccountID: u.Principal, calendarRecap: recapOf(u.CalendarRecap)})
		}
		list.UncoveredDays = agenda.UncoveredDays
		if len(list.UncoveredDays) > maxUncoveredDays {
			list.UncoveredDays, list.UncoveredDaysTotal = list.UncoveredDays[:maxUncoveredDays], len(agenda.UncoveredDays)
		}
		for _, a := range agenda.Accounts {
			ac := accountCoverage{AccountID: a.AccountID, SyncedAt: a.SyncedAt.UTC()}
			if !a.AsOf.IsZero() {
				t := a.AsOf.UTC()
				ac.CoverageAsOf = &t
			}
			list.Accounts = append(list.Accounts, ac)
		}
		if agenda.UnlinkedRecapsTotal > len(agenda.UnlinkedRecaps) {
			list.UnlinkedRecapsTotal = agenda.UnlinkedRecapsTotal
		}
		if !agenda.AsOf.IsZero() {
			t := agenda.AsOf.UTC()
			list.CoverageAsOf = &t
		}
		return list, nil
	})
}

// maxUncoveredDays caps uncovered_days; uncovered_days_total gives the full count when it is cut.
const maxUncoveredDays = 31

// accountCoverage is one account's freshness over the range of an agenda.
type accountCoverage struct {
	AccountID    string     `json:"account_id"`
	SyncedAt     time.Time  `json:"synced_at,omitzero"`
	CoverageAsOf *time.Time `json:"coverage_as_of,omitempty"`
}

func rangeOf(from, to time.Time) *rangeInfo {
	zone := from.Location().String()
	if zone == "Local" {
		zone = from.Format("MST")
	}
	return &rangeInfo{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Zone: zone}
}

// bounds resolves --from, --to and --days.
func (c *calendarCmd) bounds(rt *runtime) (from, to time.Time, err error) {
	loc := displayZone
	now := rt.now()
	fromText := c.From
	if fromText == "" {
		fromText = "today"
	}
	if from, err = parseRange("--from", fromText, now, loc); err != nil {
		return from, to, err
	}
	switch {
	case c.To != "" && c.Days != 0:
		return from, to, errs.Usage("--to and --days both set the end of the range; use one")
	case c.Days < 0:
		return from, to, errs.Usage("--days must be at least 1")
	case c.Days > 0:
		to = from.AddDate(0, 0, c.Days)
	case c.To != "":
		if to, err = parseRange("--to", c.To, now, loc); err != nil {
			return from, to, err
		}
	default:
		to = time.Date(from.Year(), from.Month(), from.Day()+1, 0, 0, 0, 0, loc)
	}
	if !to.After(from) {
		return from, to, errs.Usage("--to must be after --from; --to is exclusive")
	}
	return from, to, nil
}

// calendarAttendee is one invited person.
type calendarAttendee struct {
	Name     string `json:"name,omitempty"`
	Address  string `json:"address,omitempty"`
	Type     string `json:"type,omitempty"`
	Role     string `json:"role,omitempty"`
	Response string `json:"response,omitempty"`
}

type calendarPerson struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

type responseCounts struct {
	Accepted  int `json:"accepted"`
	Tentative int `json:"tentative"`
	Declined  int `json:"declined"`
	None      int `json:"none"`
}

type calendarAttachment struct {
	Name        string `json:"name,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type calendarSeries struct {
	Key                  string          `json:"key"`
	Rule                 json.RawMessage `json:"rule,omitempty"`
	MasterEventID        string          `json:"master_event_id,omitempty"`
	OccurrenceCountKnown int             `json:"occurrence_count_known"`
}

type recapItemOut struct {
	Title       string          `json:"title,omitempty"`
	Text        string          `json:"text,omitempty"`
	Owner       string          `json:"owner,omitempty"`
	Speaker     string          `json:"speaker,omitempty"`
	MentionedBy string          `json:"mentioned_by,omitempty"`
	At          time.Time       `json:"at,omitzero"`
	Origin      string          `json:"origin"`
	Highlights  json.RawMessage `json:"highlights,omitempty"`
}

type recapRecording struct {
	URL             string    `json:"url,omitempty"`
	Start           time.Time `json:"start,omitzero"`
	End             time.Time `json:"end,omitzero"`
	DurationSeconds int       `json:"duration_seconds,omitempty"`
	IsMissed        bool      `json:"is_missed,omitempty"`
}

type calendarRecap struct {
	CallID               string          `json:"call_id"`
	RecapID              string          `json:"recap_id,omitempty"`
	LinkMethod           string          `json:"link_method,omitempty"`
	Headline             string          `json:"headline,omitempty"`
	ShortSummary         string          `json:"short_summary,omitempty"`
	Outline              string          `json:"outline,omitempty"`
	SummarySections      json.RawMessage `json:"summary_sections,omitempty"`
	ActionItems          []recapItemOut  `json:"action_items,omitempty"`
	Mentions             []recapItemOut  `json:"mentions,omitempty"`
	Speakers             json.RawMessage `json:"speakers,omitempty"`
	Topics               json.RawMessage `json:"topics,omitempty"`
	Recording            *recapRecording `json:"recording,omitempty"`
	MeetingStart         time.Time       `json:"meeting_start,omitzero"`
	MeetingEnd           time.Time       `json:"meeting_end,omitzero"`
	ExpiresAt            time.Time       `json:"expires_at,omitzero"`
	AttendanceStatus     string          `json:"attendance_status,omitempty"`
	AttendeesCount       int             `json:"attendees_count,omitempty"`
	HasConfRoomConnected bool            `json:"has_conf_room_connected,omitempty"`
	HasCatchup           bool            `json:"has_catchup,omitempty"`
	HasRecap             bool            `json:"has_recap,omitempty"`
}

type calendarChat struct {
	ConversationID string `json:"conversation_id"`
	DisplayName    string `json:"display_name,omitempty"`
	MessageCount   int    `json:"message_count"`
}

type calendarRecording struct {
	MessageID string    `json:"message_id"`
	SentAt    time.Time `json:"sent_at"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Link      string    `json:"link,omitempty"`
	MatchedBy string    `json:"matched_by,omitempty"`
}

// unlinkedRecap is a recap with no event: the recap's own keys and the account it belongs to.
type unlinkedRecap struct {
	AccountID string `json:"account_id"`
	calendarRecap
}

// calendarExtras are the keys calendar event adds to the agenda item's.
type calendarExtras struct {
	Organizer             *calendarPerson      `json:"organizer,omitempty"`
	Attendees             []calendarAttendee   `json:"attendees,omitempty"`
	AttendeesAsOf         time.Time            `json:"attendees_as_of,omitzero"`
	ResponseCounts        *responseCounts      `json:"response_counts,omitempty"`
	BodyText              string               `json:"body_text,omitempty"`
	BodyHTML              string               `json:"body_html,omitempty"`
	BodyType              string               `json:"body_type,omitempty"`
	Attachments           []calendarAttachment `json:"attachments,omitempty"`
	Categories            []string             `json:"categories,omitempty"`
	ReminderMinutes       *int                 `json:"reminder_minutes,omitempty"`
	Series                *calendarSeries      `json:"series,omitempty"`
	Recaps                []calendarRecap      `json:"recaps,omitempty"`
	Chat                  *calendarChat        `json:"chat,omitempty"`
	Recordings            []calendarRecording  `json:"recordings,omitempty"`
	SeriesRecordings      []calendarRecording  `json:"series_recordings,omitempty"`
	SeriesRecordingsTotal int                  `json:"series_recordings_total,omitempty"`
	TextTruncated         bool                 `json:"text_truncated,omitempty"`
}

// calendarEvent is one event with everything the archive holds about it.
type calendarEvent struct {
	calendarItem
	calendarExtras
}

// eventKeys lists every key calendar event can print, for --fields validation.
func eventKeys() []string {
	return append(jsonKeys(reflect.TypeFor[calendarItem]()), jsonKeys(reflect.TypeFor[calendarExtras]())...)
}

type calendarEventCmd struct {
	Event string `arg:"" help:"An event_id, an event_key, or an unambiguous prefix of either (also the id a link made obsolete)."`
}

func (c *calendarEventCmd) Run(rt *runtime) error {
	for _, f := range rt.fields {
		if !contains(eventKeys(), f) {
			u := errs.Usage(fmt.Sprintf("unknown --fields key %q; valid keys: %s", f, strings.Join(eventKeys(), ", ")))
			u.Fix = "Pick keys from the list in the message."
			return u
		}
	}
	return rt.read("calendar event", func(st *store.Store) (result, error) {
		if st == nil {
			return &eventResult{}, nil // no archive yet: read adds needs_sync and the hint
		}
		d, err := st.CalendarEvent(rt.ctx, rt.account, c.Event)
		if errors.Is(err, store.ErrNoCalendarTables) {
			r := &eventResult{}
			r.setNeedsSync("the archive has no calendar tables yet: run teamscrawl sync")
			return r, nil
		}
		if err != nil {
			return nil, err
		}
		ev := eventOf(d)
		if rt.g.MaxText > 0 {
			ev.truncate(rt.g.MaxText)
		}
		return rt.eventResult(ev)
	})
}

// eventOf renders one event.
func eventOf(d store.CalendarDetail) *calendarEvent {
	e := d.Event
	ev := &calendarEvent{calendarItem: itemOf(d.CalendarRow)}
	x := &ev.calendarExtras
	if e.Organizer != "" || e.OrganizerAddress != "" {
		x.Organizer = &calendarPerson{Name: e.Organizer, Address: e.OrganizerAddress}
	}
	if ev.DetailLevel != calendar.DetailBasic {
		x.AttendeesAsOf = ev.DetailAsOf
	}
	var counts responseCounts
	for _, a := range d.Attendees {
		x.Attendees = append(x.Attendees, calendarAttendee(a))
		switch a.Response {
		case "accepted":
			counts.Accepted++
		case "tentative":
			counts.Tentative++
		case "declined":
			counts.Declined++
		default:
			counts.None++
		}
	}
	if len(d.Attendees) > 0 {
		x.ResponseCounts = &counts
	}
	x.BodyText, x.BodyHTML, x.BodyType = e.BodyText, e.BodyHTML, e.BodyType
	if e.AttachmentsJSON != "" {
		_ = json.Unmarshal([]byte(e.AttachmentsJSON), &x.Attachments)
	}
	if e.CategoriesJSON != "" {
		_ = json.Unmarshal([]byte(e.CategoriesJSON), &x.Categories)
	}
	x.ReminderMinutes = e.ReminderMinutes
	if s := d.Series; s.Key != "" {
		x.Series = &calendarSeries{Key: s.Key, Rule: s.Rule, MasterEventID: s.MasterID, OccurrenceCountKnown: s.Occurrences}
	}
	for _, r := range d.Recaps {
		x.Recaps = append(x.Recaps, recapOf(r))
	}
	if d.Chat != nil {
		x.Chat = &calendarChat{ConversationID: d.Chat.ConversationID, DisplayName: d.Chat.DisplayName, MessageCount: d.Chat.MessageCount}
	}
	x.Recordings = recordingsOf(d.Recordings)
	x.SeriesRecordings, x.SeriesRecordingsTotal = recordingsOf(d.SeriesRecordings), d.SeriesRecordingsTotal
	return ev
}

func recordingsOf(in []store.CalendarRecording) []calendarRecording {
	var out []calendarRecording
	for _, r := range in {
		out = append(out, calendarRecording{MessageID: r.MessageID, SentAt: r.SentAt.UTC(), Kind: r.Kind, Text: r.Text, Link: r.Link, MatchedBy: r.MatchedBy})
	}
	return out
}

func itemsOut(in []store.CalendarRecapItem) []recapItemOut {
	var out []recapItemOut
	for _, i := range in {
		out = append(out, recapItemOut{Title: i.Title, Text: i.Text, Owner: i.Owner, Speaker: i.Speaker, MentionedBy: i.MentionedBy, At: i.At.UTC(), Origin: i.Origin, Highlights: i.Highlights})
	}
	return out
}

func recapOf(r store.CalendarRecap) calendarRecap {
	out := calendarRecap{CallID: r.CallID, RecapID: r.RecapID, LinkMethod: r.LinkMethod, Headline: r.Headline, ShortSummary: r.ShortSummary, Outline: r.Outline,
		SummarySections: r.SummarySections, ActionItems: itemsOut(r.ActionItems), Mentions: itemsOut(r.Mentions), Speakers: r.Speakers, Topics: r.Topics,
		MeetingStart: r.MeetingStart.UTC(), MeetingEnd: r.MeetingEnd.UTC(), ExpiresAt: r.ExpiresAt.UTC(), AttendanceStatus: r.AttendanceStatus,
		AttendeesCount: r.AttendeesCount, HasConfRoomConnected: r.HasConfRoomConnected, HasCatchup: r.HasCatchUp, HasRecap: r.HasRecap}
	if r.RecordingURL != "" || !r.RecordingStart.IsZero() {
		out.Recording = &recapRecording{URL: r.RecordingURL, Start: r.RecordingStart.UTC(), End: r.RecordingEnd.UTC(), DurationSeconds: r.DurationSeconds, IsMissed: r.IsMissed}
	}
	return out
}

// truncate cuts the long text of an event to max characters and sets text_truncated.
func (ev *calendarEvent) truncate(max int) {
	cut := func(s *string) {
		if t, did := truncateRunes(*s, max); did {
			*s, ev.TextTruncated = t, true
		}
	}
	cut(&ev.BodyText)
	cut(&ev.BodyHTML)
	cut(&ev.BodyPreview)
	for i := range ev.Recaps {
		cut(&ev.Recaps[i].ShortSummary)
		cut(&ev.Recaps[i].Outline)
		cut(&ev.Recaps[i].Headline)
		for j := range ev.Recaps[i].ActionItems {
			cut(&ev.Recaps[i].ActionItems[j].Text)
		}
		for j := range ev.Recaps[i].Mentions {
			cut(&ev.Recaps[i].Mentions[j].Text)
		}
	}
}

// eventResult is calendar event's document: the event, or only the keys --fields kept, then the
// archive's age and any sync error like every read.
type eventResult struct {
	event *calendarEvent
	keys  []string
	meta
}

func (rt *runtime) eventResult(ev *calendarEvent) (result, error) {
	return &eventResult{event: ev, keys: rt.fields}, nil
}

// MarshalJSON prints the event's keys (all, or those --fields asked for, in that order), then the
// meta keys.
func (r *eventResult) MarshalJSON() ([]byte, error) {
	var body any = r.event
	if r.event == nil {
		body = struct{}{}
	} else if len(r.keys) > 0 {
		p, err := project(r.event, append(append([]string(nil), r.keys...), "text_truncated"))
		if err != nil {
			return nil, err
		}
		body = p
	}
	return joinJSON(body, r.meta)
}

// joinJSON merges the keys of two JSON objects, the first's first.
func joinJSON(a, b any) ([]byte, error) {
	enc := func(v any) ([]byte, error) {
		var buf strings.Builder
		e := json.NewEncoder(&buf)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			return nil, err
		}
		return []byte(strings.TrimSpace(buf.String())), nil
	}
	x, err := enc(a)
	if err != nil {
		return nil, err
	}
	y, err := enc(b)
	if err != nil {
		return nil, err
	}
	if string(y) == "{}" {
		return x, nil
	}
	if string(x) == "{}" {
		return y, nil
	}
	return []byte(string(x[:len(x)-1]) + "," + string(y[1:])), nil
}
