// Package outlookpeople maps Outlook's local people observations without
// resolving identities, reading credentials or changing application state.
package outlookpeople

import (
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

type Person struct {
	Class  uint16
	Key    uint32
	Parent uint32
	// VersionRaw is retained evidence, not an established current-copy ordering.
	VersionRaw  uint64
	Resynced    bool
	DisplayName string
	FirstName   string
	LastName    string
	Email       string
	// No independent alternate-email field has been established.
	AlternateEmail string
	TeamsID        string
	// RefreshedAt is the relevant-people list refresh, never last-contact time.
	RefreshedAt time.Time
}

type UnmappedError struct {
	Reason string
}

func (e *UnmappedError) Error() string { return "outlookpeople: " + e.Reason }

// MapPerson maps one of the two measured people layouts. Native identity
// strings remain unchanged; account attribution and resolution are separate.
func MapPerson(o hxstore.Object) (Person, error) {
	var fixed int
	switch o.Class {
	case 0xd2:
		fixed = 0x15a
	case 0x32a:
		fixed = 0xa8
	default:
		return Person{}, &UnmappedError{Reason: "unknown class"}
	}
	if int(o.Tag) != fixed || o.Len() < fixed {
		return Person{}, &UnmappedError{Reason: "unsupported layout"}
	}
	lead, _ := o.U32(104)
	base := int64(fixed) + int64(lead)
	if base > int64(o.Len()) {
		return Person{}, &UnmappedError{Reason: "string area outside object"}
	}
	key, _ := o.U32(20)
	if key == 0 {
		return Person{}, &UnmappedError{Reason: "missing object key"}
	}
	p := Person{Class: o.Class, Key: key, Resynced: o.Resynced}
	p.Parent, _ = o.U32(32)
	p.VersionRaw, _ = o.U64(112)
	type field struct {
		offset int
		name   string
		dst    *string
	}
	var fields []field
	if o.Class == 0xd2 {
		fields = []field{
			{256, "display name", &p.DisplayName},
			{264, "email", &p.Email},
			{276, "first name", &p.FirstName},
			{284, "Teams identity", &p.TeamsID},
			{332, "last name", &p.LastName},
		}
		p.RefreshedAt, _ = o.Ticks(248)
	} else {
		fields = []field{
			{140, "email", &p.Email},
			{156, "Teams identity", &p.TeamsID},
		}
	}
	for _, f := range fields {
		value, _, ok := o.PresentString(f.offset, int(base))
		if !ok {
			return Person{}, &UnmappedError{Reason: "malformed " + f.name}
		}
		*f.dst = value
	}
	if strings.TrimSpace(p.Email) == "" && strings.TrimSpace(p.TeamsID) == "" {
		return Person{}, &UnmappedError{Reason: "missing identity"}
	}
	return p, nil
}
