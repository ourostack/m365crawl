package hxbuild

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestNewHeader(t *testing.T) {
	b := New(Options{}).Bytes()
	if len(b) != FileHeaderSize || string(b[:8]) != "Nostromo" || b[8] != 'i' || binary.LittleEndian.Uint64(b[0x38:]) != 4096 {
		t.Fatalf("%x", b)
	}
	b = New(Options{Version: 'j', PageSize: 8192}).Bytes()
	if b[8] != 'j' || binary.LittleEndian.Uint64(b[0x38:]) != 8192 {
		t.Fatalf("%x", b)
	}
}

func TestNewRejectsHugePageSize(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	New(Options{PageSize: 1 << 40})
}

func TestBuilderAppends(t *testing.T) {
	b := New(Options{PageSize: 128})
	b.Pad(3)
	b.Raw([]byte{1, 2})
	if b.Len() != FileHeaderSize+5 {
		t.Fatal(b.Len())
	}
	b.AlignPage()
	if b.Len() != 128 {
		t.Fatal(b.Len())
	}
	b.AlignPage()
	if b.Len() != 128 {
		t.Fatal("already aligned")
	}
	b.Block([]byte("x"))
	b.BlockType(16, []byte("y"))
	got := b.Bytes()
	got[0] = 0 // Bytes returns a copy
	if b.Bytes()[0] != 'N' {
		t.Fatal("Bytes must copy")
	}
}

// TestEncodeBlock checks the header against the same hand-computed vector the
// reader's tests use: seven literals "FIXTURE".
func TestEncodeBlock(t *testing.T) {
	const want = "47c6e516bda0fcce056a703b6445025d" +
		"0800000008000000070000000400000000000000000000007046495854555245"
	if got := EncodeBlock(8, []byte("FIXTURE")); !bytes.Equal(got, mustHex(want)) {
		t.Fatalf("%x", got)
	}
	blk := EncodeBlock(8, bytes.Repeat([]byte{7}, 1000))
	if crc32.ChecksumIEEE(blk[4:0x20]) != binary.LittleEndian.Uint32(blk) ||
		crc32.ChecksumIEEE(blk[8:]) != binary.LittleEndian.Uint32(blk[4:]) {
		t.Fatal("checksums")
	}
}

func mustHex(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := range out {
		var v byte
		for _, c := range []byte(s[2*i : 2*i+2]) {
			v <<= 4
			switch {
			case c >= 'a':
				v |= c - 'a' + 10
			default:
				v |= c - '0'
			}
		}
		out[i] = v
	}
	return out
}

func TestClampU32Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	clampU32(-1)
}

func TestClampU32TooBig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	clampU32(1 << 32)
}

// decodeLiteral is a tiny independent decoder for the one shape LiteralLZ4 makes.
func decodeLiteral(t *testing.T, src []byte) []byte {
	t.Helper()
	n := int(src[0] >> 4)
	i := 1
	if n == 15 {
		for {
			b := src[i]
			i++
			n += int(b)
			if b != 255 {
				break
			}
		}
	}
	if len(src)-i != n {
		t.Fatalf("literal count %d, bytes %d", n, len(src)-i)
	}
	return src[i:]
}

func TestLiteralLZ4(t *testing.T) {
	for _, n := range []int{0, 1, 14, 15, 16, 269, 270, 271, 524, 525, 100000} {
		in := bytes.Repeat([]byte{0xab}, n)
		if got := decodeLiteral(t, LiteralLZ4(in)); !bytes.Equal(got, in) {
			t.Fatalf("n=%d", n)
		}
	}
}

func TestObject(t *testing.T) {
	o := NewObject(0x6b, 40, 40)
	o.PutU8(12, 1)
	o.PutU16(14, 0x0203)
	o.PutU32(16, 0x04050607)
	o.PutU64(20, 0x08090a0b0c0d0e0f)
	if o.Len() != 40 {
		t.Fatal(o.Len())
	}
	enc := o.Encode()
	if binary.LittleEndian.Uint32(enc) != 40 || len(enc) != 44 {
		t.Fatalf("%x", enc)
	}
	env := enc[4:]
	if binary.LittleEndian.Uint16(env) != 5 || binary.LittleEndian.Uint16(env[2:]) != 40 ||
		binary.LittleEndian.Uint32(env[4:]) != 40 || binary.LittleEndian.Uint16(env[8:]) != 0 ||
		binary.LittleEndian.Uint16(env[10:]) != 0x6b {
		t.Fatalf("envelope %x", env[:12])
	}
	if env[12] != 1 || binary.LittleEndian.Uint16(env[14:]) != 0x0203 || binary.LittleEndian.Uint32(env[16:]) != 0x04050607 ||
		binary.LittleEndian.Uint64(env[20:]) != 0x08090a0b0c0d0e0f {
		t.Fatalf("fields %x", env)
	}
}

func TestObjectStrings(t *testing.T) {
	o := NewObject(1, 24, 24)
	at := o.AppendString("Ab")
	if at != 24 || o.Len() != 24+6 {
		t.Fatal(at, o.Len())
	}
	o.PutStringWord(12, 20, "Cd")
	if got := binary.LittleEndian.Uint32(o.Encode()[4+12:]); got != 10 {
		t.Fatal(got)
	}
	if int(binary.LittleEndian.Uint32(o.Encode()[4+4:])) != o.Len() {
		t.Fatal("length must follow appends")
	}
	if !bytes.Equal(UTF16Z("a\U0001F600"), []byte{'a', 0, 0x3d, 0xd8, 0x00, 0xde, 0, 0}) {
		t.Fatalf("%x", UTF16Z("a\U0001F600"))
	}
}

func TestObjectMinimumSize(t *testing.T) {
	if NewObject(1, 5, 0).Len() != EnvelopeSize {
		t.Fatal("size must be raised to the envelope")
	}
}

func TestPayload(t *testing.T) {
	p := Payload(NewObject(1, 12, 12), NewObject(2, 12, 12))
	if len(p) != 32 || Payload() != nil {
		t.Fatal(len(p))
	}
}
