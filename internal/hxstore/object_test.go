package hxstore

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func obj(raw []byte) Object { return Object{Raw: raw} }

// TestObjectAccessorsBounds reads every width at and around the edges of an
// 8-byte object: the last in-range offset works and the next one does not.
func TestObjectAccessorsBounds(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	o := obj(raw)
	if o.Len() != 8 {
		t.Fatal(o.Len())
	}
	if v, ok := o.U8(7); !ok || v != 8 {
		t.Fatal(v, ok)
	}
	if v, ok := o.U16(6); !ok || v != 0x0807 {
		t.Fatal(v, ok)
	}
	if v, ok := o.U32(4); !ok || v != 0x08070605 {
		t.Fatal(v, ok)
	}
	if v, ok := o.U64(0); !ok || v != 0x0807060504030201 {
		t.Fatal(v, ok)
	}
	for _, off := range []int{-1, 8, 9, int(^uint(0) >> 1), -int(^uint(0)>>1) - 1} {
		if _, ok := o.U8(off); ok {
			t.Fatalf("U8(%d)", off)
		}
	}
	for _, off := range []int{-1, 7, 8, int(^uint(0) >> 1)} {
		if _, ok := o.U16(off); ok {
			t.Fatalf("U16(%d)", off)
		}
	}
	for _, off := range []int{-1, 5, 8} {
		if _, ok := o.U32(off); ok {
			t.Fatalf("U32(%d)", off)
		}
	}
	for _, off := range []int{-1, 1, 8} {
		if _, ok := o.U64(off); ok {
			t.Fatalf("U64(%d)", off)
		}
	}
	if _, ok := (Object{}).U8(0); ok {
		t.Fatal("empty object")
	}
	if o.has(0, -1) {
		t.Fatal("negative width")
	}
}

func TestStringAt(t *testing.T) {
	o := hxbuild.NewObject(1, 12, 12)
	a := o.AppendString("Alex Fixture")
	b := o.AppendString("")
	c := o.AppendString("Grüße 日本 \U0001F600")
	ob := obj(o.Encode()[4:])
	for _, tc := range []struct {
		off  int
		want string
	}{{a, "Alex Fixture"}, {b, ""}, {c, "Grüße 日本 \U0001F600"}} {
		got, ok := ob.StringAt(tc.off)
		if !ok || got != tc.want {
			t.Fatalf("StringAt(%d) = %q, %v", tc.off, got, ok)
		}
	}
}

func TestStringAtInvalid(t *testing.T) {
	nul := []byte{0, 0}
	cases := []struct {
		name string
		raw  []byte
		off  int
	}{
		{"negative", []byte{'a', 0, 0, 0}, -2},
		{"at end", []byte{'a', 0, 0, 0}, 4},
		{"past end", []byte{'a', 0, 0, 0}, 100},
		{"odd start", []byte{0, 'a', 0, 0, 0, 0}, 1},
		{"no terminator", []byte{'a', 0, 'b', 0}, 0},
		{"odd length, half terminator", []byte{'a', 0, 0}, 0},
		{"lone low surrogate", append([]byte{0x00, 0xdc}, nul...), 0},
		{"lone high surrogate at end", append([]byte{0x00, 0xd8}, nul...), 0},
		{"high surrogate then letter", append([]byte{0x00, 0xd8, 'a', 0}, nul...), 0},
		{"high surrogate then high", append([]byte{0x00, 0xd8, 0x00, 0xd8}, nul...), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s, ok := obj(tc.raw).StringAt(tc.off); ok {
				t.Fatalf("got %q", s)
			}
		})
	}
}

// TestStringWord covers the offset-word form: word + base, with the word read
// from the object, never assumed.
func TestStringWord(t *testing.T) {
	o := hxbuild.NewObject(1, 40, 40)
	o.PutStringWord(8, 12, "Subject Fixture")
	ob := obj(o.Encode()[4:])
	if s, ok := ob.String(8, 12); !ok || s != "Subject Fixture" {
		t.Fatal(s, ok)
	}
	if _, ok := ob.String(8, 1000); ok {
		t.Fatal("wrong base must not read a string")
	}
}

func TestStringPointerOutOfRange(t *testing.T) {
	raw := make([]byte, 16)
	o := obj(raw)
	put := func(w uint32) { binary.LittleEndian.PutUint32(raw[0:], w) }
	for _, w := range []uint32{16, 17, 0xffffffff, 0xfffffffe} {
		put(w)
		if _, ok := o.String(0, 0); ok {
			t.Fatalf("word %#x", w)
		}
	}
	put(0)
	if _, ok := o.String(14, 0); ok {
		t.Fatal("word itself out of range")
	}
	if _, ok := o.String(0, -2); ok {
		t.Fatal("negative base")
	}
	put(0xffffffff)
	if _, ok := o.String(0, int(^uint(0)>>1)); ok {
		t.Fatal("word plus huge base")
	}
	// A string that starts in range but whose terminator is beyond the object.
	put(4)
	copy(raw[4:], []byte{'a', 0, 'b', 0, 'c', 0, 'd', 0})
	copy(raw[12:], []byte{'e', 0, 'f', 0})
	if _, ok := o.String(0, 0); ok {
		t.Fatal("no terminator before the end")
	}
}

func TestTicksConversion(t *testing.T) {
	const unixEpochTicks = 621355968000000000 // 1970-01-01 in .NET ticks
	mk := func(v uint64) Object {
		raw := make([]byte, 8)
		binary.LittleEndian.PutUint64(raw, v)
		return obj(raw)
	}
	got, ok := mk(unixEpochTicks).Ticks(0)
	if !ok || !got.Equal(time.Unix(0, 0)) || got.Location() != time.UTC {
		t.Fatal(got, ok)
	}
	// 2026-10-05 12:00:00.0000001 UTC: one tick is 100 ns.
	want := time.Date(2026, 10, 5, 12, 0, 0, 100, time.UTC)
	ticks := uint64((want.Unix()+62135596800)&math.MaxInt64)*10_000_000 + 1
	if got, ok := mk(ticks).Ticks(0); !ok || !got.Equal(want) {
		t.Fatal(got, ok)
	}
	for name, v := range map[string]uint64{
		"zero":      0,
		"negative":  1 << 63,
		"max":       ^uint64(0),
		"year 1":    1,
		"year 1899": uint64(-2208988801+62135596800) * 10_000_000,
		"year 2200": uint64(7258118400+62135596800) * 10_000_000,
	} {
		if got, ok := mk(v).Ticks(0); ok {
			t.Fatalf("%s accepted: %v", name, got)
		}
	}
	// The edges inside the range.
	if _, ok := mk(uint64(-2208988800+62135596800) * 10_000_000).Ticks(0); !ok {
		t.Fatal("1900-01-01 must be accepted")
	}
	if _, ok := mk(uint64(7258118399+62135596800) * 10_000_000).Ticks(0); !ok {
		t.Fatal("last second before 2200 must be accepted")
	}
	if _, ok := mk(unixEpochTicks).Ticks(1); ok {
		t.Fatal("out of range offset")
	}
}

func envelope(l uint32, marker uint16, tag uint16, repeat uint32, zero uint16, class uint16) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], l)
	binary.LittleEndian.PutUint16(b[4:], marker)
	binary.LittleEndian.PutUint16(b[6:], tag)
	binary.LittleEndian.PutUint32(b[8:], repeat)
	binary.LittleEndian.PutUint16(b[12:], zero)
	binary.LittleEndian.PutUint16(b[14:], class)
	return b
}

type found struct {
	pos        int
	class, tag uint16
	n          int
}

func collect(p []byte) ([]found, int) {
	var got []found
	cov, _ := walkObjects(p, &Stats{}, func(pos int, class, tag uint16, raw []byte, _ bool) error {
		got = append(got, found{pos, class, tag, len(raw)})
		return nil
	})
	return got, cov
}

// TestWalkEnvelopeRepeatedLengthRule: each way an envelope can fail is
// rejected, and a good object right after it is still found.
func TestWalkEnvelopeRepeatedLengthRule(t *testing.T) {
	good := hxbuild.Payload(hxbuild.NewObject(0x6b, 20, 20))
	pad := func(b []byte, n int) []byte { return append(b, make([]byte, n)...) }
	cases := []struct {
		name string
		bad  []byte
	}{
		{"repeat differs", pad(envelope(20, 5, 20, 21, 0, 7), 4)},
		{"marker not 5", pad(envelope(20, 6, 20, 20, 0, 7), 4)},
		{"zero field not 0", pad(envelope(20, 5, 20, 20, 1, 7), 4)},
		{"length below envelope", pad(envelope(11, 5, 8, 11, 0, 7), 0)},
		{"tag above length", pad(envelope(20, 5, 21, 20, 0, 7), 4)},
		{"length past the payload", pad(envelope(1000, 5, 20, 1000, 0, 7), 4)},
		{"huge length", pad(envelope(0xffffffff, 5, 20, 0xffffffff, 0, 7), 4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := append(append([]byte(nil), tc.bad...), good...)
			got, cov := collect(p)
			if len(got) != 1 || got[0].class != 0x6b || got[0].pos != len(tc.bad) {
				t.Fatalf("found %+v", got)
			}
			if cov != len(good) {
				t.Fatalf("covered %d, want %d", cov, len(good))
			}
		})
	}
}

// TestWalkPositionsAndTiling: objects that sit back to back are all found; a
// tag equal to the length and a minimal 12-byte object are accepted.
func TestWalkPositionsAndTiling(t *testing.T) {
	p := hxbuild.Payload(
		hxbuild.NewObject(1, 12, 12),
		hxbuild.NewObject(2, 100, 100),
		hxbuild.NewObject(3, 0, 40),
	)
	p = append(p, 0xaa, 0xbb, 0xcc) // trailing bytes that are not an object
	got, cov := collect(p)
	want := []found{{0, 1, 12, 12}, {16, 2, 100, 100}, {120, 3, 0, 40}}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("object %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if cov != len(p)-3 {
		t.Fatalf("covered %d of %d", cov, len(p))
	}
}

func TestWalkShortPayloads(t *testing.T) {
	for n := 0; n < 20; n++ {
		if got, cov := collect(make([]byte, n)); len(got) != 0 || cov != 0 {
			t.Fatalf("n=%d: %v %d", n, got, cov)
		}
	}
}

func TestWalkObjectsCallbackError(t *testing.T) {
	boom := errors.New("boom")
	p := hxbuild.Payload(hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(2, 12, 12))
	calls := 0
	cov, err := walkObjects(p, &Stats{}, func(int, uint16, uint16, []byte, bool) error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 || cov != 0 {
		t.Fatal(err, calls, cov)
	}
}
