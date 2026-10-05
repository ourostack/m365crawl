package hxstore

// maxPairs caps the number of distinct (class, tag) pairs Stats records, so a
// hostile file cannot grow the map without bound. Pairs seen after the cap are
// counted in PairsOverflow.
const maxPairs = 4096

// Pair names an object class together with its envelope tag. The tag is a
// function of the class (it equals the size of the class's fixed region), so a
// class that shows up with a second tag has changed layout.
type Pair struct {
	Class uint16
	Tag   uint16
}

// Block rejection reasons, as they appear in Stats.Rejected.
const (
	RejectTruncated     = "truncated"      // the header or payload runs past the end of the file
	RejectHeaderUnknown = "header_unknown" // the constant at +0x1c is not 4
	RejectTypeOther     = "type_other"     // a block type other than 8
	RejectOversize      = "oversize"       // inflated length 0 or above the limit, or a payload larger than any valid block
	RejectHeaderCRC     = "header_crc"     // header checksum mismatch
	RejectPayloadCRC    = "payload_crc"    // payload checksum mismatch
	RejectInflate       = "inflate"        // the payload does not decode to the declared length
)

// Stats holds counts only: nothing in it is content from the store, so it is
// safe to log or print.
type Stats struct {
	// BlocksFound counts every place the block magic was found.
	BlocksFound int
	// BlocksValid counts blocks that passed every check and were walked.
	BlocksValid int
	// Rejected counts the blocks that were found but not valid, by reason.
	Rejected map[string]int
	// PayloadBytes is the total inflated size of the valid blocks.
	PayloadBytes int64
	// UnwalkedBytes is the part of PayloadBytes that no object covers.
	UnwalkedBytes int64
	// Objects counts the objects the walk found.
	Objects int
	// Pairs counts objects per (class, tag), up to a cap of 4,096 distinct pairs.
	Pairs map[Pair]int
	// PairsOverflow counts objects whose pair was new after the cap was reached.
	PairsOverflow int
}

// BlocksRejected is the total of Rejected.
func (s Stats) BlocksRejected() int {
	n := 0
	for _, c := range s.Rejected {
		n += c
	}
	return n
}

func (s *Stats) reject(reason string) {
	if s.Rejected == nil {
		s.Rejected = map[string]int{}
	}
	s.Rejected[reason]++
}

func (s *Stats) countPair(p Pair) {
	s.Objects++
	if s.Pairs == nil {
		s.Pairs = map[Pair]int{}
	}
	if _, seen := s.Pairs[p]; !seen && len(s.Pairs) >= maxPairs {
		s.PairsOverflow++
		return
	}
	s.Pairs[p]++
}
