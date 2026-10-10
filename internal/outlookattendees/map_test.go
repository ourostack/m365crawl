package outlookattendees

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func attendeeObject(key, parent uint32, name, email *string) *hxbuild.Object {
	o := hxbuild.NewObject(0x71, 0xb8, 184)
	o.PutU32(20, key)
	o.PutU32(32, parent)
	o.PutU64(112, 42)
	for _, f := range []struct {
		off int
		s   *string
	}{{152, name}, {160, email}} {
		if f.s == nil {
			continue
		}
		at := o.AppendString(*f.s)
		o.PutU32(f.off, fixtureWord(at-184))
		o.PutU32(f.off+4, fixtureWord(len(hxbuild.UTF16Z(*f.s))))
	}
	return o
}

func fixtureWord(n int) uint32 {
	if n < 0 || uint64(n) > math.MaxUint32 {
		panic("synthetic field length outside native word")
	}
	return uint32(n)
}

func text(s string) *string { return &s }

func object(o *hxbuild.Object) hxstore.Object {
	return hxstore.Object{Class: binary.LittleEndian.Uint16(o.Encode()[14:]), Tag: binary.LittleEndian.Uint16(o.Encode()[6:]), Raw: o.Encode()[4:]}
}

func TestMapLiteralObservation(t *testing.T) {
	o := object(attendeeObject(8, 9, text(" Pat \U0001f600 "), text("PAT@example.test")))
	o.BlockOffset, o.PayloadPos, o.Resynced = 77, 12, true
	a, err := MapAttendee(o)
	if err != nil || a.Key != 8 || a.DetailKey != 9 || a.VersionRaw != 42 ||
		a.BlockOffset != 77 || a.PayloadPos != 12 || !a.Resynced ||
		a.Name == nil || *a.Name != " Pat \U0001f600 " || a.Email == nil || *a.Email != "PAT@example.test" {
		t.Fatalf("literal observation lost: %+v, %v", a, err)
	}
}

func TestMapPresence(t *testing.T) {
	a, err := MapAttendee(object(attendeeObject(1, 2, nil, text(""))))
	if err != nil || a.Name != nil || a.Email == nil || *a.Email != "" {
		t.Fatalf("absent and empty conflated: %+v,%v", a, err)
	}
}

func TestMapRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*hxstore.Object)
	}{
		{"class", func(o *hxstore.Object) { o.Class = 1 }},
		{"tag", func(o *hxstore.Object) { o.Tag++ }},
		{"short", func(o *hxstore.Object) { o.Raw = o.Raw[:183] }},
		{"key", func(o *hxstore.Object) { binary.LittleEndian.PutUint32(o.Raw[20:], 0) }},
		{"parent", func(o *hxstore.Object) { binary.LittleEndian.PutUint32(o.Raw[32:], 0) }},
		{"area", func(o *hxstore.Object) { binary.LittleEndian.PutUint32(o.Raw[104:], ^uint32(0)) }},
		{"name-range", func(o *hxstore.Object) { binary.LittleEndian.PutUint32(o.Raw[152:], ^uint32(0)) }},
		{"email-range", func(o *hxstore.Object) { binary.LittleEndian.PutUint32(o.Raw[160:], ^uint32(0)) }},
		{"surrogate", func(o *hxstore.Object) { binary.LittleEndian.PutUint16(o.Raw[184:], 0xd800) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := object(attendeeObject(1, 2, text("Name"), text("a@example.test")))
			tc.edit(&o)
			a, err := MapAttendee(o)
			var unmapped *UnmappedError
			if !errors.As(err, &unmapped) || a != (Attendee{}) || unmapped.Error() == "" {
				t.Fatalf("malformed object accepted: %+v,%v", a, err)
			}
		})
	}
}

func TestMapScalarExactAndExcess(t *testing.T) {
	for _, field := range []string{"name", "email"} {
		for _, n := range []int{4, 5} {
			var name, email *string
			if field == "name" {
				name = text(strings.Repeat("x", n))
			} else {
				email = text(strings.Repeat("x", n))
			}
			a, err := mapAttendee(object(attendeeObject(1, 2, name, email)), 4)
			var size *Error
			if n == 4 && err != nil {
				t.Fatal(err)
			}
			if n == 5 && (!errors.As(err, &size) || size.Code != "outlook_attendees_too_large" || a != (Attendee{})) {
				t.Fatalf("excess %s scalar admitted: %+v,%v", field, a, err)
			}
		}
	}
}

func TestMapPublicScalarLimit(t *testing.T) {
	_, err := MapAttendee(object(attendeeObject(1, 2, text(strings.Repeat("x", 256<<10)), nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = MapAttendee(object(attendeeObject(1, 2, text(strings.Repeat("x", (256<<10)+1)), nil)))
	var size *Error
	if !errors.As(err, &size) {
		t.Fatalf("public scalar cap omitted: %v", err)
	}
}

func TestMapMalformedFieldPrecedesScalarLimit(t *testing.T) {
	o := object(attendeeObject(1, 2, text("oversized"), text("valid")))
	binary.LittleEndian.PutUint32(o.Raw[160:], ^uint32(0))
	_, err := mapAttendee(o, 4)
	var unmapped *UnmappedError
	if !errors.As(err, &unmapped) || unmapped.Reason != "email" {
		t.Fatalf("size refusal preceded native field validation: %v", err)
	}
}

func TestPrintableErrorKeepsCausePrivate(t *testing.T) {
	cause := errors.New("synthetic-private-value")
	err := &Error{Code: "outlook_attendees_read_failed", cause: cause}
	if err.Error() != "outlook_attendees_read_failed" || !errors.Is(err, cause) {
		t.Fatalf("printable/internal cause boundary failed: %v", err)
	}
}
