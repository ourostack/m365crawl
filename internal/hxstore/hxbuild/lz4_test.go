package hxbuild

import (
	"bytes"
	"testing"
)

// decodeLZ4 is a small independent decoder for raw LZ4 blocks, written from the
// public format description, so the compressor is not checked against itself.
func decodeLZ4(t *testing.T, src []byte, want int) []byte {
	t.Helper()
	var out []byte
	i := 0
	ext := func(n int) int {
		for {
			b := src[i]
			i++
			n += int(b)
			if b != 255 {
				return n
			}
		}
	}
	for i < len(src) {
		tok := src[i]
		i++
		lits := int(tok >> 4)
		if lits == 15 {
			lits = ext(lits)
		}
		out = append(out, src[i:i+lits]...)
		i += lits
		if i == len(src) {
			break
		}
		off := int(src[i]) | int(src[i+1])<<8
		i += 2
		m := int(tok & 15)
		if m == 15 {
			m = ext(m)
		}
		m += 4
		if off == 0 || off > len(out) {
			t.Fatal("bad offset")
		}
		for k := 0; k < m; k++ {
			out = append(out, out[len(out)-off])
		}
	}
	if len(out) != want {
		t.Fatalf("decoded %d, want %d", len(out), want)
	}
	return out
}

func TestCompressLZ4RoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"empty":  nil,
		"short":  []byte("abc"),
		"eleven": []byte("abcabcabcab"),
		"runs":   bytes.Repeat([]byte{7}, 70000),
		"text":   bytes.Repeat([]byte("Fixture event text, again and again. "), 40),
		"mixed":  append(bytes.Repeat([]byte("0123456789abcdef"), 30), []byte("an ending that does not repeat at all")...),
		"far":    append(append([]byte("unique-start-xyz"), make([]byte, 70000)...), []byte("unique-start-xyz")...),
		"longlits": func() []byte {
			b := make([]byte, 600)
			for i := range b {
				b[i] = byte((i*131 + i/7) & 0xff)
			}
			return b
		}(),
	}
	for name, in := range cases {
		out := CompressLZ4(in)
		if got := decodeLZ4(t, out, len(in)); !bytes.Equal(got, in) {
			t.Fatalf("%s: round trip differs", name)
		}
	}
	// The compressor must actually use matches, and match lengths past 15 + 255.
	if out := CompressLZ4(bytes.Repeat([]byte{7}, 70000)); len(out) > 400 {
		t.Fatalf("no compression: %d", len(out))
	}
}

func TestCodec(t *testing.T) {
	in := bytes.Repeat([]byte("xyzw"), 100)
	if !bytes.Equal(CodecLiteral.encode(in), LiteralLZ4(in)) || !bytes.Equal(CodecMatches.encode(in), CompressLZ4(in)) {
		t.Fatal("codec dispatch")
	}
	b := New(Options{})
	b.BlockCodec(in, CodecMatches)
	if blk := b.Bytes()[FileHeaderSize:]; len(blk) >= BlockHeaderSize+len(in) {
		t.Fatal("matches codec did not shrink the block")
	}
}
