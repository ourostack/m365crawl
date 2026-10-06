package hxstore

import (
	"encoding/binary"
	"time"
	"unicode/utf16"
)

// Envelope layout, counted from the first byte of an object (the u16 5):
// u16 5, u16 tag, u32 length (the same value as the u32 that precedes the
// object), u16 0, u16 class. The object occupies length bytes starting at the
// envelope, and the u32 before it repeats that length. Measured on a real store
// by the spike and confirmed by the controller.
const (
	// EnvelopeSize is the size of the envelope at the start of an object.
	EnvelopeSize = 12
	// lenPrefix is the size of the u32 length that precedes the envelope.
	lenPrefix = 4

	envMarker = 5
	offEnvTag = 2
	offEnvLen = 4
	offEnvZed = 8
	offEnvCls = 10
)

// Object is one validated object inside an inflated block payload. Raw aliases
// the reader's reusable block buffer and is valid only during the callback; copy
// it to keep it.
type Object struct {
	// BlockOffset is the file offset of the block that holds the object.
	BlockOffset int64
	// PayloadPos is the offset in the inflated payload of the object's length
	// word, the four bytes before Raw.
	PayloadPos int
	Class      uint16
	Tag        uint16
	// Raw is the object: it starts at the envelope (offset 0 is the u16 5) and
	// is exactly the length the envelope declares. All accessor offsets count
	// from the first byte of Raw.
	Raw []byte
	// Resynced is true if bytes were skipped between the end of the previous
	// object in this payload (or the payload start) and this object. Such an
	// object may be an envelope found inside a malformed one. See Walk.
	Resynced bool
}

// Clone returns a copy of the object whose Raw is independent of the reader's
// buffer, safe to keep after the callback returns.
func (o Object) Clone() Object {
	o.Raw = append([]byte(nil), o.Raw...)
	return o
}

// Len is the object's length in bytes, envelope included.
func (o Object) Len() int { return len(o.Raw) }

func (o Object) has(off, n int) bool {
	return off >= 0 && n >= 0 && off <= len(o.Raw) && len(o.Raw)-off >= n
}

// U8 reads one byte at off. ok is false if it lies outside the object.
func (o Object) U8(off int) (uint8, bool) {
	if !o.has(off, 1) {
		return 0, false
	}
	return o.Raw[off], true
}

// U16 reads a little-endian 16-bit value at off.
func (o Object) U16(off int) (uint16, bool) {
	if !o.has(off, 2) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(o.Raw[off:]), true
}

// U32 reads a little-endian 32-bit value at off.
func (o Object) U32(off int) (uint32, bool) {
	if !o.has(off, 4) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(o.Raw[off:]), true
}

// U64 reads a little-endian 64-bit value at off.
func (o Object) U64(off int) (uint64, bool) {
	if !o.has(off, 8) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(o.Raw[off:]), true
}

// StringAt reads the NUL-terminated UTF-16LE string that starts at off. It is
// valid only if the start is inside the object and even, a terminator is found
// before the end of the object, and the text has no unpaired surrogate. The
// terminator is not part of the result. The scan never reads past the object.
func (o Object) StringAt(off int) (string, bool) {
	if off < 0 || off%2 != 0 || off >= len(o.Raw) {
		return "", false
	}
	end := -1
	for i := off; i+1 < len(o.Raw); i += 2 {
		if o.Raw[i] == 0 && o.Raw[i+1] == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", false
	}
	units := make([]uint16, 0, (end-off)/2)
	for i := off; i < end; i += 2 {
		units = append(units, binary.LittleEndian.Uint16(o.Raw[i:]))
	}
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xdc00 && u <= 0xdfff:
			return "", false
		case u >= 0xd800 && u <= 0xdbff:
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", false
			}
			runes = append(runes, utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		default:
			runes = append(runes, rune(u))
		}
	}
	return string(runes), true
}

// String reads a 4-byte offset word at wordOff, adds base, and returns the
// NUL-terminated UTF-16LE string at that position (see StringAt). base must not
// be negative. Both the word and the sum are checked against the object length
// before anything is read.
func (o Object) String(wordOff, base int) (string, bool) {
	w, ok := o.U32(wordOff)
	if !ok || base < 0 {
		return "", false
	}
	pos := int64(w) + int64(base)
	if pos >= int64(len(o.Raw)) {
		return "", false
	}
	return o.StringAt(int(pos))
}

// .NET ticks are 100 ns units since 0001-01-01 UTC; Unix time starts this many
// seconds later.
const (
	ticksPerSecond   = 10_000_000
	unixEpochSeconds = 62_135_596_800
	minTicksUnix     = -2_208_988_800 // 1900-01-01
	maxTicksUnix     = 7_258_118_400  // 2200-01-01
)

// Ticks reads a .NET tick count (eight bytes, UTC) at off and converts it. ok is
// false for an out-of-range offset, a zero or negative count, and a date outside
// 1900 to 2200. The conversion goes through whole seconds, so it cannot overflow
// a time.Duration.
func (o Object) Ticks(off int) (time.Time, bool) {
	v, ok := o.U64(off)
	if !ok || v == 0 || v > 1<<63-1 {
		return time.Time{}, false
	}
	secs := int64(v/ticksPerSecond) - unixEpochSeconds
	if secs < minTicksUnix || secs >= maxTicksUnix {
		return time.Time{}, false
	}
	return time.Unix(secs, int64(v%ticksPerSecond)*100).UTC(), true
}

// walkObjects finds the objects in one inflated payload and calls fn for each.
// At every position it checks a u32 length L, then the envelope (u16 5, tag, the
// same L, u16 0, class), then that 12 <= L, tag <= L and the object lies inside
// the payload. A hit is reported and the scan continues after it; a miss moves
// on one byte. It returns the number of payload bytes the objects cover; resynced tells the callback whether bytes were skipped before the object (each
// object plus its length word) and the first error fn returned.
func walkObjects(p []byte, visit func(pos int, class, tag uint16, raw []byte, resynced bool) error) (int, error) {
	covered := 0
	i := 0
	prevEnd := 0 // end of the previous object, or the payload start
	for i+lenPrefix+EnvelopeSize <= len(p) {
		env := p[i+lenPrefix:]
		if binary.LittleEndian.Uint16(env) != envMarker {
			i++
			continue
		}
		l := binary.LittleEndian.Uint32(p[i:])
		if binary.LittleEndian.Uint32(env[offEnvLen:]) != l || binary.LittleEndian.Uint16(env[offEnvZed:]) != 0 {
			i++
			continue
		}
		tag := binary.LittleEndian.Uint16(env[offEnvTag:])
		remaining := int64(len(p) - i - lenPrefix)
		if l < EnvelopeSize || int64(l) > remaining || uint32(tag) > l {
			i++
			continue
		}
		raw := env[:l]
		if err := visit(i, binary.LittleEndian.Uint16(env[offEnvCls:]), tag, raw, i != prevEnd); err != nil {
			return covered, err
		}
		step := lenPrefix + len(raw)
		covered += step
		i += step
		prevEnd = i
	}
	return covered, nil
}
