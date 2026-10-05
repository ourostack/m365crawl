package calendar

import (
	"encoding/json"
	"time"
)

// clock is how an incoming copy's modification time compares with a stored reference time.
type clock int

const (
	// peers: equal, or either time missing. The incoming copy was read from the cache just now, so
	// it is treated as at least as fresh.
	peers clock = iota
	// newer: both times set and the incoming copy is strictly later.
	newer
	// stale: both times set and the incoming copy is strictly earlier.
	stale
)

func compareClock(in, ref *time.Time) clock {
	switch {
	case in == nil || ref == nil:
		return peers
	case in.After(*ref):
		return newer
	case in.Before(*ref):
		return stale
	}
	return peers
}

// laterOf returns the later of two optional times.
func laterOf(a, b *time.Time) *time.Time {
	if b != nil && (a == nil || b.After(*a)) {
		return b
	}
	return a
}

// detailString is one text field of the detail group. measure sizes a value for the peers rule;
// nil means "the incoming copy wins". online marks the fields the organizer removes when they turn
// the online meeting off.
type detailString struct {
	field   func(*Event) *string
	measure func(string) int
	online  bool
}

func byteLen(s string) int { return len(s) }

// listLen counts the elements of a JSON array; anything else measures zero.
func listLen(s string) int {
	var list []json.RawMessage
	if json.Unmarshal([]byte(s), &list) != nil {
		return 0
	}
	return len(list)
}

var detailStrings = []detailString{
	{field: func(e *Event) *string { return &e.OnlineMeetingURL }, online: true},
	{field: func(e *Event) *string { return &e.ShortJoinURL }, online: true},
	{field: func(e *Event) *string { return &e.DialInConferenceID }, online: true},
	{field: func(e *Event) *string { return &e.DialInTollNumber }, online: true},
	{field: func(e *Event) *string { return &e.TeamsThreadID }, online: true},
	{field: func(e *Event) *string { return &e.AttendeesJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.LocationsJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.BodyHTML }, measure: byteLen},
	{field: func(e *Event) *string { return &e.BodyText }, measure: byteLen},
	{field: func(e *Event) *string { return &e.BodyPreview }, measure: byteLen},
	{field: func(e *Event) *string { return &e.AttachmentsJSON }, measure: listLen},
	{field: func(e *Event) *string { return &e.RecurrenceJSON }},
}

// pickString merges one detail string by the detail clock.
func pickString(old, in string, c clock, measure func(string) int) string {
	switch {
	case in == "":
		return old
	case old == "":
		return in
	case c == newer:
		return in
	case c == stale:
		return old
	case measure != nil && measure(old) > measure(in):
		return old
	}
	return in
}

// Capture is the append-only capture rule: it returns the row to store when in is seen and old is
// what the archive holds (nil for a first sighting). The archive keeps the richest version it has
// seen while a real change still wins.
//
//   - Identity fields take the incoming value when it is non-empty.
//   - The schedule group is compared by LastModified: a stale copy changes nothing, any other
//     copy replaces every schedule value, including an empty location and a cancelled flag.
//   - The detail group is compared field by field against DetailAsOf, the modification time of the
//     copy that supplied the stored detail: an empty incoming field keeps the old value, an old
//     empty field takes the incoming one, a newer copy replaces, a stale copy keeps, and peers
//     keep the larger list or text (so a removal that arrives without a newer time is not seen).
//     The exception: a non-stale copy that says the event is not an online meeting clears the
//     meeting links it does not itself carry.
//   - A rich copy (one that states any substantive detail field) moves DetailAsOf, and
//     DetailRawJSON when it has one, to its own time whenever that time is not older than the
//     stored one, whether or not its content differs from what is stored. A thin copy, a copy
//     that carries only small fields or only a body type, and a copy with no time never claim the
//     detail clock; a stale rich copy only fills empty fields.
//   - The small fields (reminder, categories) have a statement clock each (ReminderAsOf,
//     CategoriesAsOf) that advances only when a copy states that field, so the newest copy that
//     states it wins whatever the arrival order. See captureSmall.
//   - BodyType has no meaning without a body: it travels with the body and is taken only from the
//     copy that supplies the body.
//   - Seeing an event clears RemovedAt. FirstSeenAt, SeenAt and DetailSeenAt are the store's.
func Capture(old *Event, in Event) Event {
	var base Event
	if old != nil {
		base = *old
	}
	out := base

	// Identity.
	for _, f := range []struct{ dst, src *string }{
		{&out.AccountID, &in.AccountID}, {&out.SourceID, &in.SourceID}, {&out.GlobalID, &in.GlobalID},
		{&out.ICalUID, &in.ICalUID}, {&out.SeriesKey, &in.SeriesKey}, {&out.EventType, &in.EventType},
	} {
		if *f.src != "" {
			*f.dst = *f.src
		}
	}
	if in.Source != "" {
		out.Source = in.Source
	}
	if in.OriginalStart != nil {
		out.OriginalStart = in.OriginalStart
	}

	// Schedule.
	sched := compareClock(in.LastModified, base.LastModified)
	if sched != stale {
		out.Start, out.End = in.Start, in.End
		out.AllDay, out.StartDate, out.EndDate = in.AllDay, in.StartDate, in.EndDate
		out.TimeZone, out.TimeZoneIANA, out.UTCOffset = in.TimeZone, in.TimeZoneIANA, in.UTCOffset
		out.Subject, out.Organizer, out.OrganizerAddress = in.Subject, in.Organizer, in.OrganizerAddress
		out.IsOrganizer, out.IsPrivate, out.Cancelled = in.IsOrganizer, in.IsPrivate, in.Cancelled
		out.Response, out.ShowAs = in.Response, in.ShowAs
		out.IsOnlineMeeting, out.Location = in.IsOnlineMeeting, in.Location
		out.LastModified = laterOf(base.LastModified, in.LastModified)
	}

	// Detail.
	dclock := compareClock(in.LastModified, base.DetailAsOf)
	online := sched != stale && !in.IsOnlineMeeting
	rich := false
	for _, d := range detailStrings {
		dst, src := d.field(&out), *d.field(&in)
		rich = rich || src != ""
		if d.online && online {
			*dst = src
			continue
		}
		*dst = pickString(*dst, src, dclock, d.measure)
	}
	out.HasAttachments = base.HasAttachments || in.HasAttachments
	captureBodyType(&out, base, in)
	captureSmall(&out, base, in)

	// Only a rich copy claims the detail clock, and it does whenever its time is not older than
	// the stored one: whether its content differs is irrelevant, because a later arrival stamped
	// between the two must not be able to replace newer content. The raw record moves with the
	// clock and an empty one never replaces a stored one.
	if rich && in.LastModified != nil && dclock != stale {
		out.DetailAsOf = in.LastModified
		if in.DetailRawJSON != "" {
			out.DetailRawJSON = in.DetailRawJSON
		}
	}
	out.RemovedAt = nil
	return out
}

// captureBodyType keeps the body type with the body. It is taken from the incoming copy only when
// that copy supplies a body and the merge took a body from it (or the stored row had none).
func captureBodyType(out *Event, base, in Event) {
	supplies := in.BodyHTML != "" || in.BodyText != ""
	changed := out.BodyHTML != base.BodyHTML || out.BodyText != base.BodyText
	if supplies && in.BodyType != "" && (changed || (base.BodyHTML == "" && base.BodyText == "")) {
		out.BodyType = in.BodyType
	}
}

// captureSmall merges the two small fields, each by its own statement clock. A copy states the
// categories when CategoriesJSON is non-empty ("[]" is a statement of no categories, "" is not
// stated). It states the reminder when ReminderMinutes is non-nil (zero minutes is a real value)
// or ReminderStated is set, which is how a mapper says the source reported no reminder
// (Teams isReminderSet false): then the reminder becomes nil. A statement replaces the stored
// value unless a strictly newer statement is stored; the clock keeps the latest statement time. A
// statement with no time cannot move the clock, and a missing clock on either side counts as
// peers, so the incoming copy wins as it does for the other fields.
func captureSmall(out *Event, base, in Event) {
	if in.ReminderMinutes != nil || in.ReminderStated {
		if compareClock(in.LastModified, base.ReminderAsOf) != stale {
			out.ReminderMinutes = in.ReminderMinutes
			out.ReminderAsOf = laterOf(base.ReminderAsOf, in.LastModified)
		}
	}
	if in.CategoriesJSON != "" {
		if compareClock(in.LastModified, base.CategoriesAsOf) != stale {
			out.CategoriesJSON = in.CategoriesJSON
			out.CategoriesAsOf = laterOf(base.CategoriesAsOf, in.LastModified)
		}
	}
}

// detailEqual reports whether two events hold the same detail values. The raw record is excluded,
// and so is HasAttachments: thin copies carry it as true without a list, so a change in it alone
// must not claim that detail was captured.
func detailEqual(a, b Event) bool {
	for _, d := range detailStrings {
		if *d.field(&a) != *d.field(&b) {
			return false
		}
	}
	if a.BodyType != b.BodyType || a.CategoriesJSON != b.CategoriesJSON {
		return false
	}
	if (a.ReminderMinutes == nil) != (b.ReminderMinutes == nil) {
		return false
	}
	return a.ReminderMinutes == nil || *a.ReminderMinutes == *b.ReminderMinutes
}
