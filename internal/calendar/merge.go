package calendar

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// MergeClockSkew is how far apart two sources' last-modified times for one version of an event can
// be. Assumed from 449 of 460 Outlook-versus-Teams last-modified agreements in the spike; to be
// revised from the acceptance histogram.
const MergeClockSkew = 5 * time.Second

// AllDayConsistencyWindow is the largest offset between the starts of an all-day event as two
// sources hold it: the largest offset between two time zones.
const AllDayConsistencyWindow = 14 * time.Hour

// Fill records a schedule field taken from a row other than the schedule base, so output can say
// "location from teams as of 09:00".
type Fill struct {
	Field  Field
	Source Source
	AsOf   *time.Time
}

// Override records a field both copies stated with different values, and where the merged value
// came from: the source whose value stands, or "union" when the merged list holds entries of both.
type Override struct {
	Field Field
	From  string
	AsOf  *time.Time
}

// OverrideUnion is the From of an Override whose merged list is the union of both copies' lists.
const OverrideUnion = "union"

// Merged is the one event the rows of a (principal, key) group make.
type Merged struct {
	Event
	// Sources are the sources of the rows that took part, in the fixed order teams, outlook.
	Sources []Source
	// Filled lists the schedule fields taken from a row other than the schedule base.
	Filled []Fill
	// Overridden lists the fields both copies stated with different values, with the source the
	// merged value came from. A field only one copy stated is a Fill or nothing, never listed here.
	Overridden []Override
	// LocationTexts are the location texts of the rows that took part, in schedule order. Rooms
	// reads them so a place only one copy names stays in the room list.
	LocationTexts []string
	// Removed is true when the event is gone; RemovedBy names the sources that saw it go and
	// RemovedAt is the latest such removal.
	Removed   bool
	RemovedBy []Source
	RemovedAt *time.Time
}

// Merge makes one event of the rows of one (principal, key): live and removed rows of every source,
// at most one per source. fresh is the cache_fresh_at of each source's account in the group. The
// rules, in order:
//
//   - M0 Removal. The event is removed when every row is, or when the latest removal is not
//     followed by an edit of a live row later than MergeClockSkew. A removed event merges all its
//     rows, so it still shows the last known data; a live one drops its removed rows, so a source
//     that has since lost the event adds no stale data to one that edited it later.
//   - M1 Schedule base. A row outranks another when both last-modified times are set and differ by
//     more than MergeClockSkew, the later one winning. Otherwise the newer cache wins, then Outlook.
//   - M2 Each schedule field of the base is used when the base knows it, and filled from the next
//     row that knows it otherwise, with a Fill. Unknown never overrides known.
//   - M3 The all-day block (flag, dates, instants) comes from the base when it knows the flag. When
//     it does not and another row says all-day, the whole block comes from that row only if the base
//     looks all-day (see looksAllDay) and starts within AllDayConsistencyWindow of it; otherwise the
//     flag stays unknown. A known false is filled alone.
//   - M4 Cancelled is the base's when known; otherwise a true is filled and a false is not, because
//     a false from an older row says nothing about now.
//   - M5 The other flags are the base's when known, else filled as in M2.
//   - M6 Detail. The detail base is the row with the later DetailAsOf, else the schedule base.
//     Each detail field it leaves empty is filled from another row, and the meeting links, the body
//     and the other multi-field units fill as wholes. When the merged event is known not to be an
//     online meeting and the schedule base is later than the detail base's DetailAsOf by more than
//     the skew, the join fields are not filled from another row.
//     The meeting-link unit (join URL, short join URL, dial-in, meeting chat id) belongs to the
//     Teams meeting: a Teams row that states it supplies it whole, whichever row is the detail
//     base, and another row fills it only when no Teams row states it. The noJoin rule above still
//     wins over this. Recordings and recaps attach by the meeting chat id and the event key, never by
//     which copy won, so keeping the Teams unit keeps them. An event that a Teams row
//     says is an online meeting stays one while its join link is kept (M5 does not turn it false).
//   - M7 Unknown is what every taking-part row leaves unknown.
//   - M8 Lists. The attendee list and the structured location list are unions of the copies'
//     lists, by lower-case address (display name when an attendee has none) and by lower-case
//     name. An entry both copies hold takes the values of the copy whose list clock is newer; an
//     entry only one copy holds is kept. A list the union enlarged beyond the one M6 chose is
//     recorded as a Fill. The room list read from the merged event is also the union of every
//     copy's location text pieces (see MergedRooms); the Location text itself stays the base's.
//   - M9 Overrides. A field both copies state with different values is recorded in Overridden
//     with the source whose value stands ("union" for a merged list), so a replaced value is as
//     visible as a filled one.
func Merge(rows []Event, fresh map[Source]time.Time) Merged {
	var m Merged
	if len(rows) == 0 {
		return m
	}
	m.Removed, m.RemovedBy, m.RemovedAt, rows = removal(rows)
	ordered := append([]Event(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return better(ordered[i], ordered[j], fresh) })
	base, others := ordered[0], ordered[1:]
	out := base
	for _, o := range others {
		fillIdentity(&out, o)
	}
	var fills fillRecorder
	fillSchedule(&out, base, others, &fills)
	fillAllDay(&out, base, others, &fills)
	fillFlags(&out, base, others, &fills)
	mergeDetail(&out, base, ordered)
	keepOnline(&out, ordered)
	mergeLists(&out, ordered, &fills)
	out.Unknown = mergedUnknown(ordered)
	out.RemovedAt = m.RemovedAt
	if out.LastModified == nil {
		for _, o := range others {
			out.LastModified = laterOf(out.LastModified, o.LastModified)
		}
	}
	for _, o := range ordered {
		if !o.FirstSeenAt.IsZero() && (out.FirstSeenAt.IsZero() || o.FirstSeenAt.Before(out.FirstSeenAt)) {
			out.FirstSeenAt = o.FirstSeenAt
		}
		if o.SeenAt.After(out.SeenAt) {
			out.SeenAt = o.SeenAt
		}
	}
	m.Event, m.Filled = out, fills.list
	m.Overridden = overrides(out, ordered)
	for _, o := range ordered {
		m.LocationTexts = append(m.LocationTexts, o.Location)
	}
	m.Sources = sourcesOf(ordered)
	return m
}

// removal applies M0. It returns whether the event is removed, the sources that saw it go, the
// latest removal, and the rows that take part in the merge.
func removal(rows []Event) (removed bool, by []Source, at *time.Time, take []Event) {
	var gone, live []Event
	for _, r := range rows {
		if r.RemovedAt != nil {
			gone = append(gone, r)
			at = laterOf(at, r.RemovedAt)
		} else {
			live = append(live, r)
		}
	}
	if len(gone) == 0 {
		return false, nil, nil, rows
	}
	edited := false
	for _, r := range live {
		if r.LastModified != nil && r.LastModified.After(at.Add(MergeClockSkew)) {
			edited = true
		}
	}
	if edited {
		return false, nil, nil, live
	}
	return true, sourcesOf(gone), at, rows
}

// sourcesOf lists the distinct sources of rows in the fixed order teams, outlook, then any other
// by name.
func sourcesOf(rows []Event) []Source {
	seen := map[Source]bool{}
	var out []Source
	for _, r := range rows {
		if !seen[r.Source] {
			seen[r.Source] = true
			out = append(out, r.Source)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return sourceRank(out[i]) < sourceRank(out[j]) || (sourceRank(out[i]) == sourceRank(out[j]) && out[i] < out[j])
	})
	return out
}

func sourceRank(s Source) int {
	switch s {
	case SourceTeams:
		return 0
	case SourceOutlook:
		return 1
	}
	return 2
}

// better reports whether a outranks b (M1). It is a total order only for at most two rows per key
// (one per source), which is all the calendar holds; with more rows the tie-breaks could cycle.
func better(a, b Event, fresh map[Source]time.Time) bool {
	if a.LastModified != nil && b.LastModified != nil {
		if d := a.LastModified.Sub(*b.LastModified); d > MergeClockSkew || d < -MergeClockSkew {
			return d > 0
		}
	}
	if fa, fb := fresh[a.Source], fresh[b.Source]; !fa.Equal(fb) {
		return fa.After(fb)
	}
	return a.Source == SourceOutlook && b.Source != SourceOutlook
}

// fillIdentity copies into out each identity field it leaves empty and another row has.
func fillIdentity(out *Event, o Event) {
	for _, f := range []struct{ dst, src *string }{
		{&out.GlobalID, &o.GlobalID}, {&out.ICalUID, &o.ICalUID}, {&out.SeriesKey, &o.SeriesKey}, {&out.EventType, &o.EventType},
	} {
		if *f.dst == "" {
			*f.dst = *f.src
		}
	}
	if out.OriginalStart == nil {
		out.OriginalStart = o.OriginalStart
	}
}

// fillRecorder collects the Fill records in the order the fields were filled.
type fillRecorder struct{ list []Fill }

func (r *fillRecorder) add(f Field, from Event) {
	r.list = append(r.list, Fill{Field: f, Source: from.Source, AsOf: from.LastModified})
}

// textFields are the schedule text fields M2 fills, with the location, which the Teams plan treats
// as a schedule field for the merge although the core clocks it apart.
func textFields(e *Event) []struct {
	name Field
	ptr  *string
} {
	return []struct {
		name Field
		ptr  *string
	}{
		{FieldSubject, &e.Subject}, {FieldLocation, &e.Location}, {FieldOrganizer, &e.Organizer},
		{FieldOrganizerAddress, &e.OrganizerAddress}, {FieldResponse, &e.Response}, {FieldShowAs, &e.ShowAs},
		{FieldTimeZone, &e.TimeZone}, {FieldTimeZoneIANA, &e.TimeZoneIANA}, {FieldUTCOffset, &e.UTCOffset},
	}
}

// fillSchedule is M2 for the text fields.
func fillSchedule(out *Event, base Event, others []Event, fills *fillRecorder) {
	for i, f := range textFields(out) {
		if !base.unknown(f.name) {
			continue
		}
		for _, o := range others {
			if !o.unknown(f.name) {
				*f.ptr = *textFields(&o)[i].ptr
				fills.add(f.name, o)
				break
			}
		}
	}
}

// looksAllDay reports whether an event's instants look like an all-day event held as timed: at
// least 23 hours long, and within an hour of a whole number of days.
func looksAllDay(e Event) bool {
	d := e.End.Sub(e.Start)
	if d < 23*time.Hour {
		return false
	}
	days := (d + 12*time.Hour) / (24 * time.Hour)
	off := d - days*24*time.Hour
	return off >= -time.Hour && off <= time.Hour
}

// fillAllDay is M3.
func fillAllDay(out *Event, base Event, others []Event, fills *fillRecorder) {
	if base.AllDay.Known() {
		return
	}
	for _, o := range others {
		switch {
		case o.AllDay.Is(false):
			out.AllDay = TriFalse
			fills.add(FieldAllDay, o)
			return
		case o.AllDay.Is(true) && looksAllDay(base) && within(base.Start, o.Start, AllDayConsistencyWindow):
			out.AllDay, out.StartDate, out.EndDate, out.Start, out.End = o.AllDay, o.StartDate, o.EndDate, o.Start, o.End
			fills.add(FieldAllDay, o)
			return
		}
	}
}

// fillFlags is M4 and M5.
func fillFlags(out *Event, base Event, others []Event, fills *fillRecorder) {
	baseFlags := flagFields(&base)
	for i, f := range flagFields(out) {
		if f.name == FieldAllDay || baseFlags[i].tri.Known() {
			continue
		}
		for _, o := range others {
			v := *flagFields(&o)[i].tri
			if !v.Known() || (f.name == FieldCancelled && !v.Is(true)) {
				continue
			}
			*f.tri = v
			fills.add(f.name, o)
			break
		}
	}
}

// mergeDetail is M6. ordered is the rows in schedule order, base the first.
func mergeDetail(out *Event, base Event, ordered []Event) {
	detail := base
	for _, r := range ordered[1:] {
		if r.DetailAsOf != nil && (detail.DetailAsOf == nil || r.DetailAsOf.After(*detail.DetailAsOf)) {
			detail = r
		}
	}
	// When the merged event is known not to be online and the schedule base says so later than the
	// detail was captured, the detail's meeting links are history: only the schedule base's stand.
	noJoin := out.IsOnlineMeeting.Is(false) && detail.DetailAsOf != nil && base.LastModified != nil &&
		base.LastModified.After(detail.DetailAsOf.Add(MergeClockSkew))
	// Start from the detail base's detail fields, then fill the empty ones from the other rows.
	for _, u := range units {
		if u.group == groupSchedule || u.name == "location" {
			continue
		}
		switch {
		case noJoin && u.links:
			u.assign(out, base)
		case u.links:
			if t := teamsStating(ordered, u); t != nil {
				u.assign(out, *t)
			} else {
				u.assign(out, detail)
			}
		default:
			u.assign(out, detail)
		}
	}
	out.DetailAsOf, out.DetailSeenAt, out.DetailRawJSON, out.FieldClocksJSON = detail.DetailAsOf, detail.DetailSeenAt, detail.DetailRawJSON, detail.FieldClocksJSON
	out.Unknown = detail.Unknown // so the fill below sees what the detail base left unknown
	for _, r := range ordered {
		for _, u := range units {
			if u.group == groupSchedule || u.name == "location" || u.states(*out) || !u.states(r) || (noJoin && u.links) {
				continue
			}
			u.assign(out, r)
		}
		if out.DetailRawJSON == "" {
			out.DetailRawJSON = r.DetailRawJSON
		}
	}
}

// teamsStating is the first Teams row that states unit u, or nil.
func teamsStating(rows []Event, u unit) *Event {
	for i := range rows {
		if rows[i].Source == SourceTeams && u.states(rows[i]) {
			return &rows[i]
		}
	}
	return nil
}

// keepOnline keeps an event online when a Teams row says it is and the join link is kept: the
// other copy's false is the absence of a Teams meeting in its store, not the end of the meeting.
func keepOnline(out *Event, rows []Event) {
	if !out.IsOnlineMeeting.Is(false) || out.OnlineMeetingURL == "" {
		return
	}
	for _, r := range rows {
		if r.Source == SourceTeams && r.IsOnlineMeeting.Is(true) {
			out.IsOnlineMeeting = TriTrue
			return
		}
	}
}

// comparable is a field both copies can state, for M9.
type comparable struct {
	field  Field
	states func(Event) bool
	val    func(Event) string
	list   bool
}

func comparables() []comparable {
	var out []comparable
	for i, f := range textFields(&Event{}) {
		name := f.name
		i := i
		out = append(out, comparable{field: name, states: func(e Event) bool { return !e.unknown(name) },
			val: func(e Event) string { return strings.TrimSpace(*textFields(&e)[i].ptr) }})
	}
	for i, f := range flagFields(&Event{}) {
		i := i
		out = append(out, comparable{field: f.name, states: func(e Event) bool { return flagFields(&e)[i].tri.Known() },
			val: func(e Event) string { return strconv.Itoa(int(*flagFields(&e)[i].tri)) }})
	}
	str := func(f Field, get func(Event) string, list bool) {
		out = append(out, comparable{field: f, states: func(e Event) bool { return get(e) != "" }, val: get, list: list})
	}
	str(FieldJoinURL, func(e Event) string { return e.OnlineMeetingURL }, false)
	str(FieldShortJoinURL, func(e Event) string { return e.ShortJoinURL }, false)
	str(FieldDialIn, func(e Event) string {
		if e.DialInConferenceID == "" && e.DialInTollNumber == "" {
			return ""
		}
		return e.DialInConferenceID + "|" + e.DialInTollNumber
	}, false)
	str(FieldMeetingChatID, func(e Event) string { return e.TeamsThreadID }, false)
	str(FieldAttendees, func(e Event) string { return e.AttendeesJSON }, true)
	str(FieldRooms, func(e Event) string { return e.LocationsJSON }, true)
	str(FieldBody, func(e Event) string {
		if e.BodyHTML == "" && e.BodyText == "" {
			return ""
		}
		return e.BodyHTML + "\x00" + e.BodyText
	}, false)
	str(FieldBodyPreview, func(e Event) string { return e.BodyPreview }, false)
	str(FieldAttachments, func(e Event) string { return e.AttachmentsJSON }, false)
	str(FieldCategories, func(e Event) string { return e.CategoriesJSON }, false)
	str(FieldRecurrence, func(e Event) string { return e.RecurrenceJSON }, false)
	return out
}

// overrides is M9. A row "states" a field as Fill and unknown handling do; two rows that both state
// it with different values make an override whose From is the row the merged value equals.
func overrides(out Event, rows []Event) []Override {
	if len(rows) < 2 {
		return nil
	}
	var res []Override
	for _, c := range comparables() {
		var stated []Event
		for _, r := range rows {
			if c.states(r) {
				stated = append(stated, r)
			}
		}
		differ := false
		for _, r := range stated[min(1, len(stated)):] {
			if c.val(r) != c.val(stated[0]) {
				differ = true
			}
		}
		if !differ {
			continue
		}
		merged := c.val(out)
		matched := -1
		for i, r := range stated {
			if c.val(r) == merged {
				matched = i
				break
			}
		}
		switch {
		case matched >= 0:
			res = append(res, Override{Field: c.field, From: string(stated[matched].Source), AsOf: stated[matched].LastModified})
		case c.list:
			res = append(res, Override{Field: c.field, From: OverrideUnion})
		}
	}
	return res
}

// mergedUnknown is M7: a name is unknown only if it is unknown in every row that took part.
func mergedUnknown(rows []Event) []Field {
	var out []Field
	for _, n := range unknownVocabulary {
		all := true
		for _, r := range rows {
			if !r.unknown(n) {
				all = false
				break
			}
		}
		if all {
			out = append(out, n)
		}
	}
	return NormalizeUnknown(out)
}
