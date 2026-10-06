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
	RejectMagic         = "magic"          // the magic was not where the scan found it (the file changed under the reader)
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
	// UnwalkedBytes is the part of PayloadBytes that is neither covered by an
	// object nor recognized framing (see FramingBytes).
	UnwalkedBytes int64
	// Objects counts the objects the walk found.
	Objects int
	// Pairs counts objects per (class, tag), up to a cap of 4,096 distinct pairs.
	Pairs map[Pair]int
	// ObjectsResynced counts objects reached after skipped bytes (see Walk).
	ObjectsResynced int
	// PairsResynced counts, per (class, tag), how many of the Pairs objects were
	// resynced. It has an entry only for pairs that are in Pairs.
	PairsResynced map[Pair]int
	// FramingBytes is the part of PayloadBytes that is recognized framing: the
	// 15-byte head and the 11-byte constant trailers. UnwalkedBytes is what is
	// neither an object nor recognized framing.
	FramingBytes int64
	// PayloadsNoObject counts valid payloads with no object, and NoObjectBytes
	// their total size. They are not decoded.
	PayloadsNoObject int
	NoObjectBytes    int64
	// NoObjectFirst4 classifies the first four bytes of those payloads
	// ("zero", "small" under 256, "medium" under 65536, "large").
	NoObjectFirst4 map[string]int
	// HeadLens counts payloads by the offset of their first object when it is
	// not zero; offsets of 64 or more share the key 64. HeadEndsInTrailer counts
	// heads of 11 bytes or more whose last 11 bytes are the Trailer constant, and
	// HeadFirst4 classifies the first four bytes of heads of 4 bytes or more.
	HeadLens          map[int]int
	HeadEndsInTrailer int
	HeadFirst4        map[string]int
	// GapLens counts the raw gaps between the end of one object and the start of
	// the next, by length (64 or more share the key 64). GapsTrailer counts gaps
	// that are exactly the Trailer. GapFirst4 classifies the first four bytes of
	// gaps of 4 bytes or more.
	GapLens     map[int]int
	GapsTrailer int
	GapFirst4   map[string]int
	// Tails counts payloads with bytes after their last object, TailBytes their
	// total (before any trailer is recognized), and TailsStartWithTrailer how
	// many of them start with the Trailer constant.
	Tails                 int
	TailBytes             int64
	TailsStartWithTrailer int
	// TrailerHeadLens is an exact histogram of the lengths of heads longer than
	// HeadSize that end in the Trailer, whether or not they were accepted;
	// lengths above 4,096 share the key 4,097.
	TrailerHeadLens map[int]int
	// HeadsTrailerForm counts heads longer than HeadSize that end in the
	// Trailer and were accepted as framing.
	HeadsTrailerForm int
	// Gap1Values counts one-byte gaps by the byte's value (a number).
	Gap1Values map[int]int
	// Gap11OneByteDiff counts 11-byte gaps that differ from the Trailer in
	// exactly one byte, by that byte's index; Gap11ManyDiff counts those that
	// differ in two or more.
	Gap11OneByteDiff map[int]int
	Gap11ManyDiff    int
	// PairsResyncedLong counts, per pair, resynced objects of at least
	// LongObject bytes (long enough to hold fields deep in a fixed region).
	PairsResyncedLong map[Pair]int
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

// MaxTrailerHead is the longest trailer-form head accepted as framing. It is a
// guess to be corrected from TrailerHeadLens on the real store.
const MaxTrailerHead = 64

// trailerHeadCap is the largest head length with its own TrailerHeadLens key.
const trailerHeadCap = 4096

// LongObject is the length from which PairsResyncedLong counts an object.
const LongObject = 824

func (s *Stats) countPair(p Pair, resynced bool, n int) {
	s.Objects++
	if resynced {
		s.ObjectsResynced++
	}
	if s.Pairs == nil {
		s.Pairs = map[Pair]int{}
	}
	if _, seen := s.Pairs[p]; !seen && len(s.Pairs) >= maxPairs {
		s.PairsOverflow++
		return
	}
	s.Pairs[p]++
	if resynced {
		if s.PairsResynced == nil {
			s.PairsResynced = map[Pair]int{}
		}
		s.PairsResynced[p]++
		if n >= LongObject {
			bump(&s.PairsResyncedLong, p)
		}
	}
}

func bump[K comparable](m *map[K]int, k K) {
	if *m == nil {
		*m = map[K]int{}
	}
	(*m)[k]++
}

func capLen(n int) int { return min(n, gapCap) }

// noteGap records the framing before the object that starts at i, given the end
// of the previous object (-1 for the first), and reports whether the object is
// resynced.
func (s *Stats) noteGap(p []byte, prevEnd, i int) bool {
	if prevEnd < 0 {
		if i > 0 {
			bump(&s.HeadLens, capLen(i))
			if i >= 4 {
				bump(&s.HeadFirst4, firstWordClass(p))
			}
			if hasTrailerAt(p, i-TrailerSize) {
				s.HeadEndsInTrailer++
			}
		}
		// A head is framing if it is HeadSize bytes (a length-only rule: on the
		// real store no 15-byte head ends in the Trailer), or if it is HeadSize
		// plus a multiple of 4, at most MaxTrailerHead, and ends in the Trailer
		// (measured: heads of 31, 35, 39, 43, 51 and 59 bytes; 3,802 heads end
		// in the constant). The bound stops a long run of unknown bytes that
		// happens to end in the constant from hiding a corrupt object.
		trailerForm := i > HeadSize && hasTrailerAt(p, i-TrailerSize)
		if trailerForm {
			bump(&s.TrailerHeadLens, min(i, trailerHeadCap+1))
		}
		if i == HeadSize || (trailerForm && (i-HeadSize)%4 == 0 && i <= MaxTrailerHead) {
			s.FramingBytes += int64(i)
			if i > HeadSize {
				s.HeadsTrailerForm++
			}
			return false
		}
		return i != 0
	}
	gap := i - prevEnd
	bump(&s.GapLens, capLen(gap))
	if gap == 1 {
		bump(&s.Gap1Values, int(p[prevEnd]))
	}
	if gap >= 4 {
		bump(&s.GapFirst4, firstWordClass(p[prevEnd:]))
	}
	if gap == TrailerSize && hasTrailerAt(p, prevEnd) {
		s.GapsTrailer++
		s.FramingBytes += TrailerSize
		return false
	}
	if gap == TrailerSize {
		s.noteNearTrailer(p[prevEnd : prevEnd+TrailerSize])
	}
	return gap != 0
}

// noteNearTrailer counts an 11-byte gap that is not the Trailer by how it
// differs: in exactly one byte (by index) or in more.
func (s *Stats) noteNearTrailer(g []byte) {
	idx, n := 0, 0
	for k := range g {
		if g[k] != Trailer[k] {
			idx, n = k, n+1
		}
	}
	if n == 1 {
		bump(&s.Gap11OneByteDiff, idx)
	} else {
		s.Gap11ManyDiff++
	}
}

// noteEnd records the tail after the last object (prevEnd is -1 if there was
// none) and the payload-level counts.
func (s *Stats) noteEnd(p []byte, prevEnd int) {
	if prevEnd < 0 {
		if len(p) > 0 {
			s.PayloadsNoObject++
			s.NoObjectBytes += int64(len(p))
			if len(p) >= 4 {
				bump(&s.NoObjectFirst4, firstWordClass(p))
			}
		}
		return
	}
	if tail := len(p) - prevEnd; tail > 0 {
		s.Tails++
		s.TailBytes += int64(tail)
		if hasTrailerAt(p, prevEnd) {
			s.TailsStartWithTrailer++
			s.FramingBytes += TrailerSize
		}
	}
}
