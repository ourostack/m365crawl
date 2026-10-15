// Package outlookmeetingattendees owns canonical, source-selected native
// observations. It does not resolve subjects or establish current membership.
package outlookmeetingattendees

import (
	"errors"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

const MaxEnvelopeBytes = 402653184
const MaxCensusBytes = 4194304

var (
	ErrInvalid  = errors.New("outlook_attendee_evidence_invalid")
	ErrTooLarge = errors.New("outlook_attendee_evidence_too_large")
)

type Evidence struct {
	Attendees     []outlookattendees.Attendee
	AmbiguousKeys []uint32
	Parents       []outlookattendees.Evidence
	Losses        []outlookattendees.Loss
	Selections    []outlookcal.SelectionReceipt
}

type PairCount struct {
	Class uint16 `json:"class"`
	Tag   uint16 `json:"tag"`
	Count int64  `json:"count,string"`
}

type IntCount struct {
	Key   int64 `json:"key,string"`
	Count int64 `json:"count,string"`
}

type EnumCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count,string"`
}

type Census struct {
	BlocksFound           int64       `json:"blocks_found,string"`
	BlocksValid           int64       `json:"blocks_valid,string"`
	PayloadBytes          int64       `json:"payload_bytes,string"`
	UnwalkedBytes         int64       `json:"unwalked_bytes,string"`
	Objects               int64       `json:"objects,string"`
	ObjectsResynced       int64       `json:"objects_resynced,string"`
	FramingBytes          int64       `json:"framing_bytes,string"`
	PayloadsNoObject      int64       `json:"payloads_no_object,string"`
	NoObjectBytes         int64       `json:"no_object_bytes,string"`
	HeadEndsInTrailer     int64       `json:"head_ends_in_trailer,string"`
	GapsTrailer           int64       `json:"gaps_trailer,string"`
	Tails                 int64       `json:"tails,string"`
	TailBytes             int64       `json:"tail_bytes,string"`
	TailsStartWithTrailer int64       `json:"tails_start_with_trailer,string"`
	HeadsTrailerForm      int64       `json:"heads_trailer_form,string"`
	Gap11ManyDiff         int64       `json:"gap11_many_diff,string"`
	PairsOverflow         int64       `json:"pairs_overflow,string"`
	Rejected              []EnumCount `json:"rejected"`
	NoObjectFirst4        []EnumCount `json:"no_object_first4"`
	HeadFirst4            []EnumCount `json:"head_first4"`
	GapFirst4             []EnumCount `json:"gap_first4"`
	Pairs                 []PairCount `json:"pairs"`
	PairsResynced         []PairCount `json:"pairs_resynced"`
	PairsResyncedLong     []PairCount `json:"pairs_resynced_long"`
	HeadLens              []IntCount  `json:"head_lens"`
	GapLens               []IntCount  `json:"gap_lens"`
	TrailerHeadLens       []IntCount  `json:"trailer_head_lens"`
	Gap1Values            []IntCount  `json:"gap1_values"`
	Gap11OneByteDiff      []IntCount  `json:"gap11_one_byte_diff"`
}

type Summary struct {
	AttendeesTotal, ParentsTotal, SelectionsTotal int
	AmbiguousKeys                                 []uint32
	Losses                                        []outlookattendees.Loss
}

type Visitor func(int, outlookattendees.Attendee) error
