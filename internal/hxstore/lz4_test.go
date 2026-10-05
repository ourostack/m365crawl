package hxstore

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tok builds an LZ4 token byte from a literal count and a match length code
// (each at most 15). The vectors below spell out every other byte by hand
// instead of going through a compressor.
func tok(lit, match int) byte { return byte(lit<<4 | match) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func mustDecode(t *testing.T, src []byte, want int) []byte {
	t.Helper()
	out, err := decodeBlock(nil, src, want, MaxInflated)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	return out
}

func wantErr(t *testing.T, src []byte, want int, target error) {
	t.Helper()
	out, err := decodeBlock(nil, src, want, MaxInflated)
	if !errors.Is(err, target) {
		t.Fatalf("got %v (output %d bytes), want %v", err, len(out), target)
	}
	if out != nil {
		t.Fatalf("an error must not return output")
	}
}

func readHex(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "lz4", name)) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b
}

// TestDecodeLZ4Vectors decodes blocks written by the real lz4 command-line
// compressor (level 9) from invented input. The .hex files hold the raw block,
// taken out of the lz4 frame, and the input it came from.
func TestDecodeLZ4Vectors(t *testing.T) {
	for _, name := range []string{"text", "zeros", "mixed", "short"} {
		t.Run(name, func(t *testing.T) {
			blk := readHex(t, name+".lz4.hex")
			raw := readHex(t, name+".raw.hex")
			if got := mustDecode(t, blk, len(raw)); !bytes.Equal(got, raw) {
				t.Fatalf("decoded bytes differ from the compressor's input")
			}
		})
	}
}

func TestDecodeLZ4LiteralOnly(t *testing.T) {
	got := mustDecode(t, cat([]byte{tok(5, 0)}, []byte("hello")), 5)
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeLZ4ShortMatch(t *testing.T) {
	// "abcd" then a match of length 4+2 at distance 4 (non-overlapping prefix),
	// then the closing literal-only sequence.
	src := cat([]byte{tok(4, 2)}, []byte("abcd"), []byte{4, 0}, []byte{tok(1, 0)}, []byte("Z"))
	if got := mustDecode(t, src, 11); string(got) != "abcdabcdabZ" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeLZ4OverlappingMatch(t *testing.T) {
	// One literal "a", then a match of length 4+6 at distance 1: ten more a's.
	src := cat([]byte{tok(1, 6)}, []byte("a"), []byte{1, 0}, []byte{tok(0, 0)})
	if got := mustDecode(t, src, 11); string(got) != "aaaaaaaaaaa" {
		t.Fatalf("got %q", got)
	}
	// "ab" then a match of length 4+3 at distance 2: the pattern repeats.
	src = cat([]byte{tok(2, 3)}, []byte("ab"), []byte{2, 0}, []byte{tok(0, 0)})
	if got := mustDecode(t, src, 9); string(got) != "ababababa" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeLZ4LongVarints(t *testing.T) {
	// Literal count 15+255+10 = 280 through two extension bytes.
	lits := bytes.Repeat([]byte("L"), 280)
	src := cat([]byte{tok(15, 0), 255, 10}, lits)
	if got := mustDecode(t, src, 280); !bytes.Equal(got, lits) {
		t.Fatalf("literal extension decoded wrong")
	}

	// Match length 4+15+255+255+7 = 536 at distance 1, after one literal; the
	// run is 537 bytes and the stream ends with an empty literal sequence.
	src = cat([]byte{tok(1, 15)}, []byte("x"), []byte{1, 0}, []byte{255, 255, 7}, []byte{tok(0, 0)})
	if got := mustDecode(t, src, 537); !bytes.Equal(got, bytes.Repeat([]byte("x"), 537)) {
		t.Fatalf("match extension decoded wrong")
	}

	// An extension byte of exactly 255 as the last byte before a terminating 0.
	src = cat([]byte{tok(15, 0), 255, 0}, bytes.Repeat([]byte("y"), 270))
	if got := mustDecode(t, src, 270); len(got) != 270 {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestDecodeLZ4ReusesBuffer(t *testing.T) {
	buf := make([]byte, 64)
	src := cat([]byte{tok(3, 0)}, []byte("abc"))
	out, err := decodeBlock(buf, src, 3, MaxInflated)
	if err != nil || string(out) != "abc" {
		t.Fatalf("got %q, %v", out, err)
	}
	if &out[0] != &buf[0] {
		t.Fatalf("a large enough buffer must be reused, not reallocated")
	}
}

func TestDecodeLZ4RejectsZeroDistance(t *testing.T) {
	src := cat([]byte{tok(4, 0)}, []byte("abcd"), []byte{0, 0}, []byte{tok(0, 0)})
	wantErr(t, src, 8, ErrLZ4ZeroOffset)
}

func TestDecodeLZ4RejectsDistanceBeyondOutput(t *testing.T) {
	// Four bytes written, then a match 5 back.
	src := cat([]byte{tok(4, 0)}, []byte("abcd"), []byte{5, 0}, []byte{tok(0, 0)})
	wantErr(t, src, 8, ErrLZ4Offset)
	// A match as the very first thing: nothing to point at.
	wantErr(t, []byte{tok(0, 0), 1, 0, tok(0, 0)}, 4, ErrLZ4Offset)
	// The largest 16-bit distance against a tiny output.
	wantErr(t, cat([]byte{tok(1, 0)}, []byte("a"), []byte{0xff, 0xff}, []byte{tok(0, 0)}), 5, ErrLZ4Offset)
}

func TestDecodeLZ4RejectsTruncated(t *testing.T) {
	// A stream with a literal extension, literals, a match with an extension
	// and a closing literal sequence, cut at every possible length.
	src := cat(
		[]byte{tok(15, 15), 1}, bytes.Repeat([]byte("q"), 16), []byte{1, 0}, []byte{255, 3},
		[]byte{tok(2, 0)}, []byte("zz"),
	)
	want := 16 + 4 + 15 + 255 + 3 + 2
	if got := mustDecode(t, src, want); len(got) != want {
		t.Fatalf("the whole stream must decode, got %d bytes", len(got))
	}
	for n := 0; n < len(src); n++ {
		if out, err := decodeBlock(nil, src[:n], want, MaxInflated); err == nil || out != nil {
			t.Fatalf("prefix of %d bytes decoded: %v", n, err)
		}
	}
	// The specific reasons at the interesting cut points.
	wantErr(t, nil, 1, ErrLZ4Truncated)                                  // no token
	wantErr(t, []byte{tok(15, 0)}, 20, ErrLZ4Truncated)                  // literal extension missing
	wantErr(t, []byte{tok(15, 0), 255}, 400, ErrLZ4Truncated)            // extension ends on 255
	wantErr(t, []byte{tok(3, 0), 'a', 'b'}, 3, ErrLZ4Truncated)          // literals cut
	wantErr(t, []byte{tok(1, 0), 'a', 1}, 8, ErrLZ4Truncated)            // distance half there
	wantErr(t, []byte{tok(1, 15), 'a', 1, 0}, 40, ErrLZ4Truncated)       // match extension missing
	wantErr(t, []byte{tok(1, 15), 'a', 1, 0, 255}, 700, ErrLZ4Truncated) // match extension ends on 255
	wantErr(t, []byte{tok(1, 1), 'a', 1, 0}, 6, ErrLZ4Truncated)         // no closing sequence
}

func TestDecodeLZ4RejectsTrailing(t *testing.T) {
	good := cat([]byte{tok(3, 0)}, []byte("abc"))
	mustDecode(t, good, 3)
	// One extra byte after the closing literals reads as the start of a match.
	wantErr(t, cat(good, []byte{0}), 3, ErrLZ4Truncated)
	// A whole extra sequence after the output is full must not be accepted.
	wantErr(t, cat(good, []byte{1, 0}, []byte{tok(1, 0)}, []byte("z")), 3, ErrLZ4Overrun)
	// Extra bytes after the closing literal-only sequence of a longer stream.
	wantErr(t, cat([]byte{tok(1, 1)}, []byte("a"), []byte{1, 0}, []byte{tok(0, 0)}, []byte{9}), 6, ErrLZ4Truncated)
}

func TestDecodeLZ4InflatedSizeDisagrees(t *testing.T) {
	src := cat([]byte{tok(5, 0)}, []byte("hello"))
	wantErr(t, src, 6, ErrLZ4Short)   // header promises more than the stream holds
	wantErr(t, src, 4, ErrLZ4Overrun) // header promises less
	wantErr(t, src, 0, ErrLZ4Overrun)
	// A match that would run past the declared size.
	wantErr(t, cat([]byte{tok(1, 4)}, []byte("a"), []byte{1, 0}, []byte{tok(0, 0)}), 5, ErrLZ4Overrun)
	// Literal extension bytes that claim more than the output can hold.
	wantErr(t, []byte{tok(15, 0), 255, 255, 255}, 10, ErrLZ4Overrun)
}

func TestDecodeLZ4NeverExceedsWant(t *testing.T) {
	// A hostile run of 0xff extension bytes must stop at the declared size and
	// must not grow with the input.
	src := cat([]byte{tok(15, 0)}, bytes.Repeat([]byte{255}, 1<<16))
	wantErr(t, src, 100, ErrLZ4Overrun)
	src = cat([]byte{tok(1, 15)}, []byte("a"), []byte{1, 0}, bytes.Repeat([]byte{255}, 1<<16))
	wantErr(t, src, 100, ErrLZ4Overrun)
}

func TestDecodeLZ4RejectsOversizeBeforeAllocating(t *testing.T) {
	src := []byte{tok(0, 0)}
	for _, tc := range []struct{ want, limit int }{
		{MaxInflated + 1, MaxInflated},
		{MaxInflated + 1, MaxInflated + 100}, // limit is capped at MaxInflated
		{10, 9},
		{-1, 100},
	} {
		if out, err := decodeBlock(nil, src, tc.want, tc.limit); !errors.Is(err, ErrLZ4Size) || out != nil {
			t.Fatalf("want %d limit %d: %v", tc.want, tc.limit, err)
		}
	}
	// The cap itself is allowed to be asked for (and fails only on content).
	if _, err := decodeBlock(nil, src, 0, 0); err != nil {
		t.Fatalf("zero size with an empty closing sequence must decode: %v", err)
	}
}
