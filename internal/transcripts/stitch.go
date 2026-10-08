package transcripts

import (
	"sort"
	"time"
)

// StitchPart is one part of a call with what the archive holds of its transcript. State is the
// stored state of the last fetch attempt, empty when there was none; FetchedAt is when the text in
// Entries was fetched, nil when the archive holds no text of the part.
type StitchPart struct {
	Part
	Entries   []Entry
	FetchedAt *time.Time
	State     string
}

// Segment is one part's stretch of a call's transcript. A part with no text is a segment with no
// entries and the reason, so a gap keeps its place.
type Segment struct {
	Ordinal        int
	TranscribeOnly bool
	StartsAt       time.Time
	FetchedAt      *time.Time
	State, Reason  string
	Entries        []Entry
}

// Stitch lays a call's parts end to end in ordinal order, one segment per part.
func Stitch(parts []StitchPart) []Segment {
	ps := append([]StitchPart(nil), parts...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Ordinal < ps[j].Ordinal })
	out := make([]Segment, len(ps))
	for i, p := range ps {
		state := PartState(p.Part, p.State, p.FetchedAt != nil)
		seg := Segment{Ordinal: p.Ordinal, TranscribeOnly: p.TranscribeOnly, StartsAt: p.StartsAt, State: state, Reason: Reason(p.Part, state)}
		if state == StateOK {
			seg.FetchedAt, seg.Entries = p.FetchedAt, p.Entries
		}
		out[i] = seg
	}
	return out
}
