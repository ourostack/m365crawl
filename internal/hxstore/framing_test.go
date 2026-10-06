package hxstore

import (
	"bytes"
	"context"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

// head15 is a 15-byte head whose last 11 bytes are the trailer constant (the
// first four are arbitrary here: the meaning of the head is not known).
func head15() []byte { return append([]byte{1, 0, 0, 0}, hxbuild.Trailer...) }

func walkPayload(t *testing.T, p []byte) (Stats, []bool) {
	t.Helper()
	b := hxbuild.New(hxbuild.Options{})
	b.Block(p)
	var flags []bool
	st, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(o Object) error {
		flags = append(flags, o.Resynced)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, flags
}

func objs(n int) []*hxbuild.Object {
	var out []*hxbuild.Object
	for i := 0; i < n; i++ {
		out = append(out, hxbuild.NewObject(uint16((i+1)&0xffff), 12, 12))
	}
	return out
}

func TestConstantsMatchBuilder(t *testing.T) {
	if !bytes.Equal(Trailer[:], hxbuild.Trailer) || TrailerSize != len(hxbuild.Trailer) {
		t.Fatal("reader and builder disagree on the trailer")
	}
}

// TestFramedPayloadIsContiguous: a head, objects each followed by the trailer,
// nothing resynced, every non-object byte is framing.
func TestFramedPayloadIsContiguous(t *testing.T) {
	st, flags := walkPayload(t, hxbuild.FramedPayload(head15(), objs(3)...))
	if st.Objects != 3 || st.ObjectsResynced != 0 || flags[0] || flags[1] || flags[2] {
		t.Fatalf("%+v %v", st, flags)
	}
	if st.FramingBytes != 15+3*11 || st.UnwalkedBytes != 0 {
		t.Fatalf("framing %d unwalked %d", st.FramingBytes, st.UnwalkedBytes)
	}
	if st.HeadLens[15] != 1 || st.HeadEndsInTrailer != 1 || st.HeadFirst4["small"] != 1 ||
		st.GapLens[11] != 2 || st.GapsTrailer != 2 || len(st.GapFirst4) != 1 || st.GapFirst4["zero"] != 2 ||
		st.Tails != 1 || st.TailBytes != 11 || st.TailsStartWithTrailer != 1 || st.PayloadsNoObject != 0 {
		t.Fatalf("%+v", st)
	}
	if st.FramingBytes+st.UnwalkedBytes+int64(3*16) != st.PayloadBytes {
		t.Fatal("bytes do not add up")
	}
}

func TestHeadOtherThanFifteenIsResynced(t *testing.T) {
	st, flags := walkPayload(t, append([]byte{7, 7, 7, 7, 7, 7, 7, 7}, hxbuild.Payload(objs(2)...)...))
	if !flags[0] || flags[1] || st.ObjectsResynced != 1 || st.FramingBytes != 0 || st.UnwalkedBytes != 8 ||
		st.HeadLens[8] != 1 || st.HeadEndsInTrailer != 0 || st.HeadFirst4["large"] != 1 {
		t.Fatalf("%+v %v", st, flags)
	}
	// Short and long heads: no first-word class under 4 bytes; 64 or more share a bucket.
	st, _ = walkPayload(t, append([]byte{1, 2}, hxbuild.Payload(objs(1)...)...))
	if len(st.HeadFirst4) != 0 || st.HeadLens[2] != 1 {
		t.Fatalf("%+v", st)
	}
	st, _ = walkPayload(t, append(make([]byte, 100), hxbuild.Payload(objs(1)...)...))
	if st.HeadLens[64] != 1 || st.HeadFirst4["zero"] != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestPayloadStartingWithObjectHasNoHead(t *testing.T) {
	st, flags := walkPayload(t, hxbuild.Payload(objs(1)...))
	if flags[0] || len(st.HeadLens) != 0 || st.Tails != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestGapsThatAreNotTheTrailer(t *testing.T) {
	notTrailer := append([]byte(nil), hxbuild.Trailer...)
	notTrailer[10] = 2
	long := append([]byte{0, 0, 1, 0, 3, 0, 0, 1}, make([]byte, 80)...)
	var p []byte
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, notTrailer...) // 11 bytes, not the constant
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, long...) // long gap
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, 0, 1, 0, 0, 0xaa) // 5 bytes, "medium"
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, 2, 1, 0, 0) // 4 bytes
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, 9) // 1 byte, below the first-word minimum
	p = append(p, hxbuild.Payload(objs(1)...)...)
	p = append(p, 1, 2, 3) // tail, no trailer
	st, flags := walkPayload(t, p)
	wantFlags := []bool{false, true, true, true, true, true}
	for i := range wantFlags {
		if flags[i] != wantFlags[i] {
			t.Fatalf("flags %v", flags)
		}
	}
	if st.GapsTrailer != 0 || st.GapLens[11] != 1 || st.GapLens[64] != 1 || st.GapLens[5] != 1 || st.GapLens[4] != 1 || st.GapLens[1] != 1 ||
		st.GapFirst4["large"] != 1 || st.GapFirst4["zero"] != 1 || st.GapFirst4["medium"] != 2 || st.GapFirst4["small"] != 0 ||
		st.Tails != 1 || st.TailBytes != 3 || st.TailsStartWithTrailer != 0 || st.FramingBytes != 0 {
		t.Fatalf("%+v", st)
	}
	if want := int64(len(p) - 6*16); st.UnwalkedBytes != want {
		t.Fatalf("unwalked %d, want %d", st.UnwalkedBytes, want)
	}
}

func TestPayloadsWithoutObjects(t *testing.T) {
	st, _ := walkPayload(t, append([]byte{0, 0, 0, 0, 0, 0, 0, 0}, make([]byte, 100)...))
	if st.PayloadsNoObject != 1 || st.NoObjectBytes != 108 || st.NoObjectFirst4["zero"] != 1 || st.Objects != 0 || st.UnwalkedBytes != 108 {
		t.Fatalf("%+v", st)
	}
	st, _ = walkPayload(t, []byte{1, 2})
	if st.PayloadsNoObject != 1 || len(st.NoObjectFirst4) != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestNoteEndEmptyPayload(t *testing.T) {
	var st Stats
	st.noteEnd(nil, -1)
	if st.PayloadsNoObject != 0 {
		t.Fatal("an empty payload is not a payload without objects")
	}
}

func TestFirstWordClass(t *testing.T) {
	for want, b := range map[string][]byte{
		"zero": {0, 0, 0, 0}, "small": {255, 0, 0, 0}, "medium": {0, 1, 0, 0}, "large": {0, 0, 1, 0},
	} {
		if got := firstWordClass(b); got != want {
			t.Fatalf("%v: %s", b, got)
		}
	}
	if hasTrailerAt(hxbuild.Trailer, -1) || hasTrailerAt(hxbuild.Trailer, 1) || !hasTrailerAt(hxbuild.Trailer, 0) {
		t.Fatal("hasTrailerAt")
	}
}

func TestHeadTrailerFormAndNearTrailerGaps(t *testing.T) {
	// A 31-byte head ending in the trailer is framing; a 31-byte head that does
	// not is resynced.
	good := append(make([]byte, 20), hxbuild.Trailer...)
	st, flags := walkPayload(t, append(good, hxbuild.Payload(objs(1)...)...))
	if flags[0] || st.FramingBytes != 31 || st.HeadsTrailerForm != 1 || st.UnwalkedBytes != 0 {
		t.Fatalf("%+v", st)
	}
	st, flags = walkPayload(t, append(make([]byte, 31), hxbuild.Payload(objs(1)...)...))
	if !flags[0] || st.HeadsTrailerForm != 0 || st.FramingBytes != 0 {
		t.Fatalf("%+v", st)
	}
	// Gaps: one byte, an 11-byte gap one byte off at index 10, one off at 0, and
	// one that differs in many places.
	one := append([]byte(nil), hxbuild.Trailer...)
	one[10] = 2
	zero := append([]byte(nil), hxbuild.Trailer...)
	zero[0] = 9
	many := make([]byte, 11)
	var p []byte
	for _, g := range [][]byte{{7}, one, zero, many} {
		p = append(p, hxbuild.Payload(objs(1)...)...)
		p = append(p, g...)
	}
	p = append(p, hxbuild.Payload(objs(1)...)...)
	st, _ = walkPayload(t, p)
	if st.Gap1Values[7] != 1 || st.Gap11OneByteDiff[10] != 1 || st.Gap11OneByteDiff[0] != 1 || st.Gap11ManyDiff != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestResyncedLong(t *testing.T) {
	long, short := hxbuild.NewObject(1, 12, LongObject), hxbuild.NewObject(2, 12, 12)
	p := append([]byte{0xff}, hxbuild.Payload(long)...)
	p = append(p, 0xff)
	p = append(p, hxbuild.Payload(short)...)
	st, _ := walkPayload(t, p)
	if st.ObjectsResynced != 2 || st.PairsResyncedLong[Pair{1, 12}] != 1 || len(st.PairsResyncedLong) != 1 {
		t.Fatalf("%+v", st)
	}
}
