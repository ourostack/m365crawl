package hxstore

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"testing"
)

// vectorBlock is a complete block computed outside this code, with Python's
// zlib.crc32, so the checksum ranges are pinned by arithmetic and not by the
// reader agreeing with itself. Layout, offsets in hex:
//
//	00 header crc  = crc32(bytes 04..20) = 0x16e5c647
//	04 payload crc = crc32(bytes 08..28+8) = 0xcefca0bd  (payload is 8 bytes)
//	08 magic       05 6a 70 3b 64 45 02 5d
//	10 type 8, 14 payload length 8, 18 inflated length 7, 1c constant 4
//	20 eight zero bytes
//	28 payload: token 0x70 (seven literals, no match) then "FIXTURE"
const vectorBlock = "47c6e516bda0fcce056a703b6445025d" +
	"0800000008000000070000000400000000000000000000007046495854555245"

func vectorBytes() []byte {
	b, err := hex.DecodeString(vectorBlock)
	if err != nil {
		panic(err)
	}
	return b
}

func vector(t *testing.T) []byte {
	t.Helper()
	return vectorBytes()
}

// fix recomputes both checksums after a test edits a block, so a test can break
// one field without tripping a checksum, or break a checksum on purpose.
func fix(b []byte) []byte {
	end := HeaderSize + int(binary.LittleEndian.Uint32(b[offPayloadLen:]))
	if end > len(b) {
		end = len(b)
	}
	binary.LittleEndian.PutUint32(b[offPayloadCRC:], crc32.ChecksumIEEE(b[8:end]))
	binary.LittleEndian.PutUint32(b[offHeaderCRC:], crc32.ChecksumIEEE(b[4:0x20]))
	return b
}

func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }

// TestBlockHeaderCRCRange: the vector parses, which pins the header checksum to
// exactly bytes 04..20 (the arithmetic is in the vector's comment). The flips
// check the edges of that range from both sides.
func TestBlockHeaderCRCRange(t *testing.T) {
	b := vector(t)
	blk, err := ParseBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	if blk.Type != 8 || blk.InflatedLen != 7 || blk.Len() != 0x30 {
		t.Fatalf("parsed %+v", blk)
	}
	// Inside the range: byte 0x04 (the first one) and the checksum field itself.
	for _, at := range []int{0x00, 0x04} {
		c := append([]byte(nil), b...)
		c[at] ^= 1
		if _, err := ParseBlock(c); !errors.Is(err, ErrBlockHeaderCRC) {
			t.Fatalf("flip at %#x: %v", at, err)
		}
	}
	// Byte 0x20 is the first byte past the range: the header checksum still
	// matches and the payload checksum, which does cover it, is what fails.
	c := append([]byte(nil), b...)
	c[0x20] ^= 1
	if _, err := ParseBlock(c); !errors.Is(err, ErrBlockPayloadCRC) {
		t.Fatalf("flip at 0x20: %v", err)
	}
}

// TestBlockPayloadCRCRange: the payload checksum covers 08..28+length, so the
// last payload byte counts and bytes after the block do not.
func TestBlockPayloadCRCRange(t *testing.T) {
	b := vector(t)
	c := append([]byte(nil), b...)
	c[len(c)-1] ^= 1
	if _, err := ParseBlock(c); !errors.Is(err, ErrBlockPayloadCRC) {
		t.Fatalf("last payload byte: %v", err)
	}
	c = append([]byte(nil), b...)
	c[offPayloadCRC] ^= 1
	if _, err := ParseBlock(c); !errors.Is(err, ErrBlockHeaderCRC) {
		t.Fatalf("the payload checksum field lies inside the header range: %v", err)
	}
	long := append(append([]byte(nil), b...), 0xde, 0xad)
	blk, err := ParseBlock(long)
	if err != nil || blk.Len() != len(b) {
		t.Fatalf("bytes after the block must be ignored: %v", err)
	}
	// A payload checksum that is wrong while the header checksum is right.
	c = append([]byte(nil), b...)
	c[len(c)-2] ^= 1
	if _, err := ParseBlock(c); !errors.Is(err, ErrBlockPayloadCRC) {
		t.Fatalf("got %v", err)
	}
}

func TestParseBlockRejections(t *testing.T) {
	good := vector(t)
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrBlockTruncated},
		{"short header", good[:HeaderSize-1], ErrBlockTruncated},
		{"magic", mut(func(b []byte) []byte { b[offMagic] ^= 1; return fix(b) }), ErrBlockMagic},
		{"constant", mut(func(b []byte) []byte { put32(b, offConstant, 5); return fix(b) }), ErrBlockHeaderUnknown},
		{"type other", mut(func(b []byte) []byte { put32(b, offType, 16); return fix(b) }), ErrBlockTypeOther},
		{"inflated zero", mut(func(b []byte) []byte { put32(b, offInflated, 0); return fix(b) }), ErrBlockOversize},
		{"inflated max plus one", mut(func(b []byte) []byte { put32(b, offInflated, MaxInflated+1); return fix(b) }), ErrBlockOversize},
		{"payload cut", good[:len(good)-1], ErrBlockTruncated},
		{"payload length huge", mut(func(b []byte) []byte { put32(b, offPayloadLen, 0xffffffff); return fix(b) }), ErrBlockTruncated},
		{"header crc", mut(func(b []byte) []byte { b[0] ^= 1; return b }), ErrBlockHeaderCRC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseBlock(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestParseBlockOrder pins the order of the checks: when two things are wrong,
// the earlier one in the documented list is the one reported.
func TestParseBlockOrder(t *testing.T) {
	good := vector(t)
	b := append([]byte(nil), good...)
	put32(b, offType, 16)
	put32(b, offInflated, 0)
	put32(b, offPayloadLen, 0xffffffff)
	b[0] ^= 1 // header checksum wrong too, and not repaired
	if _, err := ParseBlock(b); !errors.Is(err, ErrBlockTypeOther) {
		t.Fatalf("type must come before size, truncation and checksums: %v", err)
	}
	put32(b, offType, 8)
	if _, err := ParseBlock(b); !errors.Is(err, ErrBlockOversize) {
		t.Fatalf("size must come before truncation: %v", err)
	}
	put32(b, offInflated, 7)
	if _, err := ParseBlock(b); !errors.Is(err, ErrBlockTruncated) {
		t.Fatalf("truncation must come before checksums: %v", err)
	}
	put32(b, offPayloadLen, 8)
	if _, err := ParseBlock(b); !errors.Is(err, ErrBlockHeaderCRC) {
		t.Fatalf("header checksum must come before the payload checksum: %v", err)
	}
}

func TestVerifyBlockInflates(t *testing.T) {
	blk, out, err := VerifyBlock(vector(t), nil, MaxInflated)
	if err != nil || string(out) != "FIXTURE" || blk.InflatedLen != 7 {
		t.Fatalf("got %q, %v", out, err)
	}
	buf := make([]byte, 32)
	_, out, err = VerifyBlock(vector(t), buf, 7)
	if err != nil || &out[0] != &buf[0] {
		t.Fatalf("the caller's buffer must be reused: %v", err)
	}
}

func TestVerifyBlockRejections(t *testing.T) {
	if _, _, err := VerifyBlock(nil, nil, MaxInflated); !errors.Is(err, ErrBlockTruncated) {
		t.Fatalf("got %v", err)
	}
	// The caller allows less than the header declares.
	if _, _, err := VerifyBlock(vector(t), nil, 6); !errors.Is(err, ErrBlockOversize) {
		t.Fatalf("got %v", err)
	}
	// Inflated length disagrees with the stream, in both directions; the
	// checksums are repaired so only the length is wrong.
	for _, declared := range []uint32{6, 8} {
		b := append([]byte(nil), vector(t)...)
		put32(b, offInflated, declared)
		_, _, err := VerifyBlock(fix(b), nil, MaxInflated)
		if !errors.Is(err, ErrBlockInflate) {
			t.Fatalf("declared %d: %v", declared, err)
		}
		wantSpecific := ErrLZ4Overrun
		if declared == 8 {
			wantSpecific = ErrLZ4Short
		}
		if !errors.Is(err, wantSpecific) {
			t.Fatalf("declared %d: %v does not wrap %v", declared, err, wantSpecific)
		}
	}
	// A hand-built block whose Inflated field exceeds the cap is stopped by the
	// decoder before it allocates.
	if _, err := (Block{InflatedLen: MaxInflated + 1, Payload: []byte{0}}).Inflate(nil, MaxInflated+1); !errors.Is(err, ErrLZ4Size) {
		t.Fatalf("got %v", err)
	}
}

func TestReaderVersion(t *testing.T) {
	if ReaderVersion < 1 {
		t.Fatal("ReaderVersion must be positive")
	}
	if !bytes.Equal(blockMagic[:], []byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}) {
		t.Fatal("block magic changed")
	}
}
