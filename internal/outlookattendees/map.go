package outlookattendees

import "github.com/ourostack/m365crawl/internal/hxstore"

func MapAttendee(o hxstore.Object) (Attendee, error) {
	return mapAttendee(o, 256<<10)
}

func mapAttendee(o hxstore.Object, scalarLimit int) (Attendee, error) {
	if o.Class != 0x71 || o.Tag != 0xb8 || o.Len() < 184 {
		return Attendee{}, &UnmappedError{Reason: "layout"}
	}
	key, _ := o.U32(20)
	parent, _ := o.U32(32)
	if key == 0 || parent == 0 {
		return Attendee{}, &UnmappedError{Reason: "identity"}
	}
	version, _ := o.U64(112)
	lead, _ := o.U32(104)
	base := int64(184) + int64(lead)
	if base > int64(o.Len()) {
		return Attendee{}, &UnmappedError{Reason: "string_area"}
	}
	a := Attendee{Key: key, DetailKey: parent, VersionRaw: version, BlockOffset: o.BlockOffset, PayloadPos: o.PayloadPos, Resynced: o.Resynced}
	oversized := false
	for _, f := range []struct {
		offset int
		label  string
		dst    **string
	}{{152, "name", &a.Name}, {160, "email", &a.Email}} {
		value, present, ok := o.PresentString(f.offset, int(base))
		if !ok {
			return Attendee{}, &UnmappedError{Reason: f.label}
		}
		if len(value) > scalarLimit {
			oversized = true
		}
		if present {
			*f.dst = &value
		}
	}
	if oversized {
		return Attendee{}, &Error{Code: "outlook_attendees_too_large"}
	}
	return a, nil
}
