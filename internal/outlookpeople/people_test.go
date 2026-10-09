package outlookpeople

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func personObject(class, tag uint16) *hxbuild.Object {
	o := hxbuild.NewObject(class, tag, int(tag))
	o.PutU32(20, 71)
	o.PutU32(32, 9)
	o.PutU64(112, 23)
	return o
}

func personString(o *hxbuild.Object, off, base int, value string) {
	o.PutStringWord(off, base, value)
	n := len(hxbuild.UTF16Z(value))
	if n < 0 || int64(n) > math.MaxUint32 {
		panic("synthetic string length outside uint32")
	}
	o.PutU32(off+4, uint32(n))
}

func personRaw(o *hxbuild.Object) hxstore.Object {
	raw := o.Encode()[4:]
	return hxstore.Object{
		Class: binary.LittleEndian.Uint16(raw[10:]),
		Tag:   binary.LittleEndian.Uint16(raw[2:]),
		Raw:   raw,
	}
}

func relevantPerson() *hxbuild.Object {
	o := personObject(0xd2, 0x15a)
	o.PutU32(104, 16)
	base := 346 + 16
	o.Append(make([]byte, 16))
	personString(o, 256, base, "Pat Example")
	personString(o, 264, base, "Pat@example.test")
	personString(o, 276, base, "Pat")
	personString(o, 284, base, "8:orgid:synthetic-person")
	personString(o, 332, base, "Example")
	o.PutU64(248, 639029198450000000)
	return o
}

func pairPerson() *hxbuild.Object {
	o := personObject(0x32a, 0xa8)
	personString(o, 140, 168, "pair@example.test")
	personString(o, 156, 168, "8:orgid:synthetic-pair")
	return o
}

func TestMapPersonRelevantFields(t *testing.T) {
	got, err := MapPerson(personRaw(relevantPerson()))
	want := Person{
		Class: 0xd2, Key: 71, Parent: 9, VersionRaw: 23,
		DisplayName: "Pat Example", FirstName: "Pat", LastName: "Example",
		Email: "Pat@example.test", TeamsID: "8:orgid:synthetic-person",
		RefreshedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err != nil || got != want {
		t.Fatalf("mapped person = %#v, error %v; want %#v", got, err, want)
	}
}

func TestMapPersonPairs(t *testing.T) {
	raw := personRaw(pairPerson())
	raw.Resynced = true
	got, err := MapPerson(raw)
	want := Person{
		Class: 0x32a, Key: 71, Parent: 9, VersionRaw: 23, Resynced: true,
		Email: "pair@example.test", TeamsID: "8:orgid:synthetic-pair",
	}
	if err != nil || got != want {
		t.Fatalf("mapped pair = %#v, error %v; want %#v", got, err, want)
	}
}

func TestMapPersonAbsentOptional(t *testing.T) {
	o := personObject(0xd2, 0x15a)
	personString(o, 264, 346, "only@example.test")
	got, err := MapPerson(personRaw(o))
	if err != nil || got.Email != "only@example.test" || got.TeamsID != "" ||
		got.DisplayName != "" || got.FirstName != "" || got.LastName != "" ||
		got.AlternateEmail != "" || !got.RefreshedAt.IsZero() {
		t.Fatalf("absent fields = %#v, error %v", got, err)
	}
}

func TestMapPersonDoesNotReadFirstNameLengthAsAlternateEmail(t *testing.T) {
	o := relevantPerson()
	o.PutU32(280, 0)
	got, err := MapPerson(personRaw(o))
	if err != nil || got.FirstName != "" || got.AlternateEmail != "" || got.Email != "Pat@example.test" {
		t.Fatalf("overlapping field = %#v, error %v", got, err)
	}
}

func TestMapPersonIdentityHalves(t *testing.T) {
	for _, tc := range []struct {
		name  string
		email string
		mri   string
	}{
		{"email only", "solo@example.test", ""},
		{"identity only", "", "8:orgid:synthetic-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := personObject(0x32a, 0xa8)
			if tc.email != "" {
				personString(o, 140, 168, tc.email)
			}
			if tc.mri != "" {
				personString(o, 156, 168, tc.mri)
			}
			got, err := MapPerson(personRaw(o))
			if err != nil || got.Email != tc.email || got.TeamsID != tc.mri {
				t.Fatalf("identity half = %#v, error %v", got, err)
			}
		})
	}
}

func TestMapPersonUnicodeAndFlaggedLength(t *testing.T) {
	o := personObject(0x32a, 0xa8)
	personString(o, 140, 168, "例😀@example.test")
	o.PutU32(144, 34|1<<31)
	got, err := MapPerson(personRaw(o))
	if err != nil || got.Email != "例😀@example.test" {
		t.Fatalf("unicode field = %#v, error %v", got, err)
	}
}

func TestMapPersonMalformedStrings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*hxbuild.Object)
	}{
		{"odd length", func(o *hxbuild.Object) { o.PutU32(144, 3) }},
		{"outside offset", func(o *hxbuild.Object) { o.PutU32(140, math.MaxUint32) }},
		{"outside length", func(o *hxbuild.Object) { o.PutU32(144, 1<<30) }},
		{"unterminated", func(o *hxbuild.Object) {
			raw := o.Encode()[4:]
			off := int(binary.LittleEndian.Uint32(raw[140:])) + 168
			length := int(binary.LittleEndian.Uint32(raw[144:]))
			o.PutU16(off+length-2, 'x')
		}},
		{"early nul", func(o *hxbuild.Object) {
			raw := o.Encode()[4:]
			off := int(binary.LittleEndian.Uint32(raw[140:])) + 168
			o.PutU16(off, 0)
		}},
		{"unpaired surrogate", func(o *hxbuild.Object) {
			raw := o.Encode()[4:]
			off := int(binary.LittleEndian.Uint32(raw[140:])) + 168
			o.PutU16(off, 0xdc00)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := pairPerson()
			tc.mutate(o)
			got, err := MapPerson(personRaw(o))
			var unmapped *UnmappedError
			if !errors.As(err, &unmapped) || got != (Person{}) {
				t.Fatalf("malformed field returned %#v, error %v", got, err)
			}
			if strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), "synthetic-pair") {
				t.Fatalf("error leaked fixture payload: %v", err)
			}
		})
	}
}

func TestMapPersonMalformedOptionalField(t *testing.T) {
	o := relevantPerson()
	o.PutU32(260, 3)
	_, err := MapPerson(personRaw(o))
	var unmapped *UnmappedError
	if !errors.As(err, &unmapped) {
		t.Fatalf("malformed optional name error = %v", err)
	}
}

func TestMapPersonUnknownLayout(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  hxstore.Object
	}{
		{"unknown class", hxstore.Object{Class: 0x89, Tag: 0xea}},
		{"unknown relevant tag", hxstore.Object{Class: 0xd2, Tag: 0x15b}},
		{"unknown pair tag", hxstore.Object{Class: 0x32a, Tag: 0xa9}},
		{"short relevant region", hxstore.Object{Class: 0xd2, Tag: 0x15a, Raw: make([]byte, 345)}},
		{"short pair region", hxstore.Object{Class: 0x32a, Tag: 0xa8, Raw: make([]byte, 167)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MapPerson(tc.raw)
			var unmapped *UnmappedError
			if !errors.As(err, &unmapped) {
				t.Fatalf("layout error = %v", err)
			}
		})
	}
}

func TestMapPersonBadBaseOrKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  int
		word uint32
	}{
		{"huge lead", 104, math.MaxUint32},
		{"base outside object", 104, 100000},
		{"zero key", 20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := pairPerson()
			o.PutU32(tc.off, tc.word)
			_, err := MapPerson(personRaw(o))
			var unmapped *UnmappedError
			if !errors.As(err, &unmapped) {
				t.Fatalf("base/key error = %v", err)
			}
		})
	}
}

func TestMapPersonMissingIdentity(t *testing.T) {
	for _, email := range []string{"", " \t"} {
		o := personObject(0x32a, 0xa8)
		if email != "" {
			personString(o, 140, 168, email)
		}
		_, err := MapPerson(personRaw(o))
		var unmapped *UnmappedError
		if !errors.As(err, &unmapped) {
			t.Fatalf("missing identity error = %v", err)
		}
	}
}

func TestMapPersonDoesNotInventContactTime(t *testing.T) {
	for _, ticks := range []uint64{0, math.MaxUint64, 1} {
		o := relevantPerson()
		o.PutU64(248, ticks)
		got, err := MapPerson(personRaw(o))
		if err != nil || !got.RefreshedAt.IsZero() {
			t.Fatalf("unknown refresh ticks %d = %#v, error %v", ticks, got, err)
		}
	}
}

func TestMapPersonNativeKeyBoundary(t *testing.T) {
	o := pairPerson()
	o.PutU32(20, math.MaxUint32)
	got, err := MapPerson(personRaw(o))
	if err != nil || got.Key != math.MaxUint32 {
		t.Fatalf("native key boundary = %#v, error %v", got, err)
	}
}
