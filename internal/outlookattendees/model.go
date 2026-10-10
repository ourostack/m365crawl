// Package outlookattendees preserves native full-attendee observations without
// inferring current membership, account ownership or response roles.
package outlookattendees

import "github.com/ourostack/m365crawl/internal/hxstore"

type Attendee struct {
	Key, DetailKey uint32
	VersionRaw     uint64
	BlockOffset    int64
	PayloadPos     int
	Resynced       bool
	Name, Email    *string
}

type Evidence struct {
	DetailKey                uint32
	DetailCopies, EventLinks int
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Attendees     []Attendee
	AmbiguousKeys []uint32
	Evidence      []Evidence
	Losses        []Loss
	Stats         hxstore.Stats
}

type Error struct {
	Code  string
	cause error
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error { return e.cause }

type UnmappedError struct{ Reason string }

func (e *UnmappedError) Error() string { return "outlookattendees: " + e.Reason }
