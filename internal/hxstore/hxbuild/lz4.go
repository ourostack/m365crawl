package hxbuild

import "encoding/binary"

// Codec chooses how a block payload is compressed.
type Codec int

const (
	// CodecLiteral writes the payload as one literal-only LZ4 block: always
	// valid, never smaller.
	CodecLiteral Codec = iota
	// CodecMatches writes a greedy-matching LZ4 block, so the reader's match
	// path runs on generated data.
	CodecMatches
)

func (c Codec) encode(src []byte) []byte {
	if c == CodecMatches {
		return CompressLZ4(src)
	}
	return LiteralLZ4(src)
}

// LZ4 block format limits (public): a match is at least four bytes, the last
// five bytes of the input are always literals, and the last match must start at
// least twelve bytes before the end.
const (
	lz4MinMatch   = 4
	lz4LastLits   = 5
	lz4MatchLimit = 12
	lz4MaxOffset  = 65535
)

// CompressLZ4 encodes src as one raw LZ4 block with greedy matching: at each
// position it takes the most recent earlier occurrence of the next four bytes
// and extends it as far as it goes. It is small and slow on purpose; it exists
// to give the decoder's match path generated input, not to compress well.
func CompressLZ4(src []byte) []byte {
	var out []byte
	table := map[uint32]int{}
	anchor, i := 0, 0
	for i+lz4MatchLimit <= len(src) {
		key := binary.LittleEndian.Uint32(src[i:])
		cand, seen := table[key]
		table[key] = i
		if !seen || i-cand > lz4MaxOffset {
			i++
			continue
		}
		n := lz4MinMatch
		for i+n < len(src)-lz4LastLits && src[cand+n] == src[i+n] {
			n++
		}
		out = appendSequence(out, src[anchor:i], i-cand, n)
		i += n
		anchor = i
	}
	return appendSequence(out, src[anchor:], 0, 0)
}

// appendSequence writes one sequence: the literals, and unless matchLen is zero
// the match distance and length.
func appendSequence(out, lits []byte, offset, matchLen int) []byte {
	token := byte(min(len(lits), 15) << 4)
	if matchLen > 0 {
		token |= byte(min(matchLen-lz4MinMatch, 15)) //nolint:gosec // at most 15
	}
	out = append(out, token)
	if len(lits) >= 15 {
		out = appendExt(out, len(lits)-15)
	}
	out = append(out, lits...)
	if matchLen == 0 {
		return out
	}
	out = binary.LittleEndian.AppendUint16(out, uint16(offset)) //nolint:gosec // CompressLZ4 only matches within 65535 bytes
	if matchLen-lz4MinMatch >= 15 {
		out = appendExt(out, matchLen-lz4MinMatch-15)
	}
	return out
}

// appendExt writes a length extension: 255 for each full 255, then the rest.
func appendExt(out []byte, n int) []byte {
	for ; n >= 255; n -= 255 {
		out = append(out, 255)
	}
	return append(out, byte(n)) //nolint:gosec // n < 255 here
}
