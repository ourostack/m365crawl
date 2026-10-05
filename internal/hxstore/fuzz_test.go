package hxstore

import (
	"bytes"
	"testing"
)

// fuzzLimit keeps the fuzz targets fast: the decoder is told it may inflate at
// most this much, which also exercises the "what the caller allows" cap.
const fuzzLimit = 1 << 20

// naiveLZ4 is a deliberately simple second decoder used only as a fuzz oracle.
// It grows its output with append and checks the same rules one byte at a time.
func naiveLZ4(src []byte, want int) ([]byte, bool) {
	var out []byte
	i := 0
	next := func() (byte, bool) {
		if i >= len(src) {
			return 0, false
		}
		b := src[i]
		i++
		return b, true
	}
	length := func(n int) (int, bool) {
		if n < 15 {
			return n, true
		}
		for {
			b, ok := next()
			if !ok {
				return 0, false
			}
			n += int(b)
			if n > 1<<30 {
				return 0, false
			}
			if b != 255 {
				return n, true
			}
		}
	}
	for {
		tk, ok := next()
		if !ok {
			return nil, false
		}
		n, ok := length(int(tk >> 4))
		if !ok || n > len(src)-i || len(out)+n > want {
			return nil, false
		}
		out = append(out, src[i:i+n]...)
		i += n
		if i == len(src) {
			return out, len(out) == want
		}
		lo, ok1 := next()
		hi, ok2 := next()
		dist := int(lo) | int(hi)<<8
		if !ok1 || !ok2 || dist == 0 || dist > len(out) {
			return nil, false
		}
		m, ok := length(int(tk & 15))
		m += 4
		if !ok || len(out)+m > want {
			return nil, false
		}
		for k := 0; k < m; k++ {
			out = append(out, out[len(out)-dist])
		}
	}
}

func FuzzDecodeLZ4(f *testing.F) {
	f.Add([]byte{tok(5, 0), 'h', 'e', 'l', 'l', 'o'}, int32(5))
	f.Add([]byte{tok(1, 6), 'a', 1, 0, tok(0, 0)}, int32(11))
	f.Add([]byte{tok(15, 0), 255, 10}, int32(280))
	f.Add([]byte{tok(4, 0), 'a', 'b', 'c', 'd', 0, 0, tok(0, 0)}, int32(8))
	f.Add([]byte{tok(1, 15), 'x', 1, 0, 255, 255, 7, tok(0, 0)}, int32(537))
	f.Fuzz(func(t *testing.T, src []byte, want int32) {
		out, err := decodeBlock(nil, src, int(want), fuzzLimit)
		ref, refOK := naiveLZ4(src, int(want))
		if err != nil {
			if out != nil {
				t.Fatalf("error with output: %v", err)
			}
			if refOK && want >= 0 && int(want) <= fuzzLimit {
				t.Fatalf("decoder rejected a stream the oracle accepts: %v", err)
			}
			return
		}
		if len(out) != int(want) || int(want) > fuzzLimit {
			t.Fatalf("got %d bytes for want %d", len(out), want)
		}
		if !refOK || !bytes.Equal(out, ref) {
			t.Fatalf("decoder and oracle disagree")
		}
		// A reused buffer gives the same bytes.
		again, err := decodeBlock(make([]byte, len(out)+8), src, int(want), fuzzLimit)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("buffer reuse changed the result: %v", err)
		}
	})
}

func FuzzVerifyBlock(f *testing.F) {
	good := vectorBytes()
	f.Add(good)
	f.Add(good[:HeaderSize])
	f.Add(good[:len(good)-1])
	f.Add(append(append([]byte(nil), good...), 1, 2, 3))
	bad := append([]byte(nil), good...)
	bad[HeaderSize] ^= 0xff
	f.Add(bad)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		blk, out, err := VerifyBlock(b, nil, fuzzLimit)
		if err != nil {
			if out != nil {
				t.Fatalf("error with output: %v", err)
			}
			return
		}
		if blk.Len() > len(b) || blk.Type != BlockTypeData || len(out) != blk.InflatedLen || len(out) > fuzzLimit || len(out) < 1 {
			t.Fatalf("inconsistent result %+v, %d bytes", blk, len(out))
		}
	})
}
