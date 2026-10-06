package calendar

import (
	"fmt"
	"sort"
	"strings"
)

// Field is the stable name of an event field in the "unknown" vocabulary. The names are also the
// JSON and column vocabulary of the output built on this package.
type Field string

// The non-flag fields a source can leave unknown (Event.Unknown).
const (
	FieldSubject          Field = "subject"
	FieldLocation         Field = "location"
	FieldOrganizer        Field = "organizer"
	FieldOrganizerAddress Field = "organizer_address"
	FieldResponse         Field = "response"
	FieldShowAs           Field = "show_as"
	FieldTimeZone         Field = "time_zone"
	FieldTimeZoneIANA     Field = "time_zone_iana"
	FieldUTCOffset        Field = "utc_offset"
	FieldJoinURL          Field = "join_url"
	FieldShortJoinURL     Field = "short_join_url"
	FieldDialIn           Field = "dial_in"
	FieldMeetingChatID    Field = "meeting_chat_id"
	FieldAttendees        Field = "attendees"
	FieldRooms            Field = "rooms"
	FieldBody             Field = "body"
	FieldBodyPreview      Field = "body_preview"
	FieldAttachments      Field = "attachments"
	FieldCategories       Field = "categories"
	FieldRecurrence       Field = "recurrence"
	FieldReminder         Field = "reminder"
)

// The flags, named in output by their Tri. They are never in Event.Unknown.
const (
	FieldAllDay          Field = "all_day"
	FieldIsOrganizer     Field = "is_organizer"
	FieldIsPrivate       Field = "is_private"
	FieldCancelled       Field = "cancelled"
	FieldIsOnlineMeeting Field = "is_online_meeting"
	FieldHasAttachments  Field = "has_attachments"
)

// DetailCollapsed is the one name UnknownFields prints when every detail field is unknown.
const DetailCollapsed = "detail"

// unknownVocabulary lists the names Event.Unknown may hold.
var unknownVocabulary = []Field{
	FieldSubject, FieldLocation, FieldOrganizer, FieldOrganizerAddress, FieldResponse, FieldShowAs,
	FieldTimeZone, FieldTimeZoneIANA, FieldUTCOffset, FieldJoinURL, FieldShortJoinURL, FieldDialIn,
	FieldMeetingChatID, FieldAttendees, FieldRooms, FieldBody, FieldBodyPreview, FieldAttachments,
	FieldCategories, FieldRecurrence, FieldReminder,
}

// DetailFields are the fields a thin copy of an event lacks: the meeting links, the lists, the body
// and the small detail fields.
var DetailFields = []Field{
	FieldJoinURL, FieldShortJoinURL, FieldDialIn, FieldMeetingChatID, FieldAttendees, FieldRooms,
	FieldBody, FieldBodyPreview, FieldAttachments, FieldCategories, FieldRecurrence, FieldReminder,
	FieldHasAttachments,
}

func validField(f Field) bool {
	for _, v := range unknownVocabulary {
		if v == f {
			return true
		}
	}
	return false
}

// NormalizeUnknown returns fields sorted without duplicates; nil for none.
func NormalizeUnknown(fields []Field) []Field {
	if len(fields) == 0 {
		return nil
	}
	out := append([]Field(nil), fields...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 1
	for _, f := range out[1:] {
		if f != out[n-1] {
			out[n] = f
			n++
		}
	}
	return out[:n]
}

// unknownSet is a set of field names.
type unknownSet map[Field]bool

func setOf(fields []Field) unknownSet {
	s := unknownSet{}
	for _, f := range fields {
		s[f] = true
	}
	return s
}

func (s unknownSet) list() []Field {
	var out []Field
	for f := range s {
		out = append(out, f)
	}
	return NormalizeUnknown(out)
}

// unknown reports whether the event's source left f unknown. Flags are not looked up here.
func (e Event) unknown(f Field) bool {
	for _, u := range e.Unknown {
		if u == f {
			return true
		}
	}
	return false
}

// flagFields lists the six flags with the Event field that holds each.
func flagFields(e *Event) []struct {
	name Field
	tri  *Tri
} {
	return []struct {
		name Field
		tri  *Tri
	}{
		{FieldAllDay, &e.AllDay}, {FieldIsOrganizer, &e.IsOrganizer}, {FieldIsPrivate, &e.IsPrivate},
		{FieldCancelled, &e.Cancelled}, {FieldIsOnlineMeeting, &e.IsOnlineMeeting}, {FieldHasAttachments, &e.HasAttachments},
	}
}

// UnknownFields is the output list of what e does not know: every unknown flag and every field of
// e.Unknown, sorted. When every detail field is unknown it prints the single name "detail" in their
// place, so a thin copy costs one token. It returns nil when nothing is unknown.
func UnknownFields(e Event) []string {
	set := setOf(e.Unknown)
	for _, f := range flagFields(&e) {
		if !f.tri.Known() {
			set[f.name] = true
		}
	}
	collapse := true
	for _, f := range DetailFields {
		if !set[f] {
			collapse = false
			break
		}
	}
	if collapse {
		for _, f := range DetailFields {
			delete(set, f)
		}
		set[DetailCollapsed] = true
	}
	var out []string
	for f := range set {
		out = append(out, string(f))
	}
	sort.Strings(out)
	return out
}

// UnknownColumn is the stored form of Event.Unknown: sorted names, comma-joined.
func unknownColumn(fields []Field) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = string(f)
	}
	return strings.Join(parts, ",")
}

// parseUnknown reads the stored form; a name outside the vocabulary is damage.
func parseUnknown(text string) ([]Field, error) {
	if text == "" {
		return nil, nil
	}
	var out []Field
	for _, name := range strings.Split(text, ",") {
		f := Field(name)
		if !validField(f) {
			return nil, fmt.Errorf("calendar: stored unknown field %q is not in the vocabulary", name)
		}
		out = append(out, f)
	}
	return NormalizeUnknown(out), nil
}
