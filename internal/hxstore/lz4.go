package hxstore

import (
	"encoding/binary"
	"errors"
)

// MaxInflated is the largest decompressed block the reader accepts, 32 MiB. It is
// checked before any buffer is allocated. The public reverse-engineering notes
// report the same guard inside Outlook's own reader.
const MaxInflated = 32 << 20

// LZ4 decode failures. Every one is a distinct sentinel so callers and tests can
// tell a truncated stream from a corrupt one with errors.Is.
var (
	// ErrLZ4Size reports a declared inflated size that is negative or above the
	// allowed limit. No buffer is allocated.
	ErrLZ4Size = errors.New("hxstore: lz4: inflated size not allowed")
	// ErrLZ4Truncated reports that the input ended inside a sequence.
	ErrLZ4Truncated = errors.New("hxstore: lz4: input truncated")
	// ErrLZ4ZeroOffset reports a match whose back-reference distance is zero.
	ErrLZ4ZeroOffset = errors.New("hxstore: lz4: match offset is zero")
	// ErrLZ4Offset reports a match that points before the start of the output.
	ErrLZ4Offset = errors.New("hxstore: lz4: match offset before start of output")
	// ErrLZ4Overrun reports output that would exceed the declared inflated size.
	ErrLZ4Overrun = errors.New("hxstore: lz4: output exceeds declared size")
	// ErrLZ4Short reports a stream that ended with less output than declared.
	ErrLZ4Short = errors.New("hxstore: lz4: output shorter than declared size")
)

// decodeBlock decodes one raw LZ4 block (the block format, not the framed format
// the lz4 command writes) from src into exactly want bytes.
//
// The format is public and small: a sequence is a token byte (high nibble literal
// count, low nibble match length minus four, 15 meaning "add extension bytes"),
// the literal bytes, a little-endian 16-bit distance and the match length
// extension. The final sequence carries literals only. The container layout
// around it was learned from ukd1/hxstore-reverse-engineering (MIT licence); no
// code was copied from it.
//
// src is untrusted. Every read and copy is bounds-checked, want is checked
// against MaxInflated and limit before anything is allocated, and the output can
// never grow past want. dst is a reusable buffer: when its capacity is at least
// want it is used as is, otherwise a new buffer of exactly want bytes is made.
// The result aliases dst's storage and has length want. Any malformed input,
// including a stream that produces more or fewer than want bytes or leaves
// trailing input, returns an error and never a partial result.
func decodeBlock(dst, src []byte, want, limit int) ([]byte, error) {
	if limit > MaxInflated {
		limit = MaxInflated
	}
	if want < 0 || want > limit {
		return nil, ErrLZ4Size
	}
	out := dst
	if cap(out) < want {
		out = make([]byte, want)
	}
	out = out[:want]
	si, di := 0, 0
	for {
		if si >= len(src) {
			return nil, ErrLZ4Truncated
		}
		token := src[si]
		si++

		lits := int(token >> 4)
		if lits == 15 {
			var err error
			if lits, si, err = extend(lits, src, si, want-di); err != nil {
				return nil, err
			}
		}
		if lits > len(src)-si {
			return nil, ErrLZ4Truncated
		}
		if lits > want-di {
			return nil, ErrLZ4Overrun
		}
		di += copy(out[di:], src[si:si+lits])
		si += lits
		if si == len(src) {
			if di != want {
				return nil, ErrLZ4Short
			}
			return out, nil
		}

		if len(src)-si < 2 {
			return nil, ErrLZ4Truncated
		}
		offset := int(binary.LittleEndian.Uint16(src[si:]))
		si += 2
		if offset == 0 {
			return nil, ErrLZ4ZeroOffset
		}
		if offset > di {
			return nil, ErrLZ4Offset
		}
		matchLen := int(token & 15)
		if matchLen == 15 {
			var err error
			if matchLen, si, err = extend(matchLen, src, si, want-di); err != nil {
				return nil, err
			}
		}
		matchLen += 4
		if matchLen > want-di {
			return nil, ErrLZ4Overrun
		}
		di = copyMatch(out, di, offset, matchLen)
	}
}

// extend reads the 255-continuation length bytes that follow a nibble of 15. The
// running total is checked against room after every byte, so a hostile run of
// 0xff bytes stops at the first byte that pushes it past the output that is left
// and the sum can never overflow.
func extend(n int, src []byte, si, room int) (int, int, error) {
	for {
		if si >= len(src) {
			return 0, si, ErrLZ4Truncated
		}
		b := int(src[si])
		si++
		n += b
		if n > room+4 {
			return 0, si, ErrLZ4Overrun
		}
		if b != 255 {
			return n, si, nil
		}
	}
}

// copyMatch appends matchLen bytes copied from offset bytes back and returns the
// new output length. A distance shorter than the length overlaps its own output
// (it repeats a pattern), so it is copied forward one byte at a time; the caller
// has already checked offset <= di and di+matchLen <= len(out).
func copyMatch(out []byte, di, offset, matchLen int) int {
	if offset >= matchLen {
		copy(out[di:di+matchLen], out[di-offset:])
		return di + matchLen
	}
	for i := 0; i < matchLen; i++ {
		out[di+i] = out[di-offset+i]
	}
	return di + matchLen
}
