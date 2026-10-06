package hxstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

var errBoom = errors.New("boom")

// failReader fails (or comes up short) for reads that match its predicate.
type failReader struct {
	r     io.ReaderAt
	when  func(off int64, n int) bool
	short bool // return a short read with a nil error instead of an error
	eof   bool // return a short read with io.EOF
}

func (f failReader) ReadAt(p []byte, off int64) (int, error) {
	if f.when(off, len(p)) {
		switch {
		case f.short:
			return len(p) - 1, nil
		case f.eof:
			return len(p) - 1, io.EOF
		}
		return 0, errBoom
	}
	return f.r.ReadAt(p, off)
}

func mustOpen(t *testing.T, b []byte) *Store {
	t.Helper()
	s, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type rec struct {
	block      int64
	pos        int
	class, tag uint16
	raw        []byte
}

func walkAll(t *testing.T, s *Store) ([]rec, Stats) {
	t.Helper()
	var out []rec
	st, err := s.Walk(context.Background(), WalkOptions{}, func(o Object) error {
		out = append(out, rec{o.BlockOffset, o.PayloadPos, o.Class, o.Tag, append([]byte(nil), o.Raw...)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, st
}

func TestOpen(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	s := mustOpen(t, b.Bytes())
	if s.Version != 'i' || s.PageSize != 4096 || s.Size() != int64(b.Len()) {
		t.Fatalf("%+v", s)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsNotHxStore(t *testing.T) {
	good := hxbuild.New(hxbuild.Options{}).Bytes()
	bad := append([]byte(nil), good...)
	bad[0] = 'n'
	for name, in := range map[string][]byte{
		"empty": nil, "short": good[:FileHeaderSize-1], "magic": bad,
	} {
		if _, err := Open(bytes.NewReader(in), int64(len(in))); !errors.Is(err, ErrNotHxStore) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOpenRejectsVersion(t *testing.T) {
	for _, v := range []byte{'h', 'j', 0xff} {
		b := hxbuild.New(hxbuild.Options{Version: v}).Bytes()
		_, err := Open(bytes.NewReader(b), int64(len(b)))
		var ev ErrStoreVersion
		if !errors.As(err, &ev) || ev.Found != v || err.Error() == "" {
			t.Fatalf("version %#x: %v", v, err)
		}
	}
}

func TestOpenRejectsPageSize(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{PageSize: 8192}).Bytes()
	_, err := Open(bytes.NewReader(b), int64(len(b)))
	var ep ErrPageSize
	if !errors.As(err, &ep) || ep.Found != 8192 || err.Error() == "" {
		t.Fatal(err)
	}
}

func TestOpenReadErrors(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{}).Bytes()
	all := func(int64, int) bool { return true }
	for name, fr := range map[string]failReader{
		"error": {r: bytes.NewReader(b), when: all},
		"short": {r: bytes.NewReader(b), when: all, short: true},
		"eof":   {r: bytes.NewReader(b), when: all, eof: true},
	} {
		if _, err := Open(fr, int64(len(b))); err == nil || errors.Is(err, ErrNotHxStore) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A reader that returns the whole header together with io.EOF is fine.
	eofAtEnd := eofReader{b}
	if _, err := Open(eofAtEnd, int64(len(b))); err != nil {
		t.Fatal(err)
	}
}

type eofReader struct{ b []byte }

func (e eofReader) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, e.b[off:])
	if n < len(p) || int(off)+n == len(e.b) {
		return n, io.EOF
	}
	return n, nil
}

func TestOpenFile(t *testing.T) {
	dir := t.TempDir()
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	path := filepath.Join(dir, "store")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	objs, st := walkAll(t, s)
	if len(objs) != 1 || st.BlocksValid != 1 {
		t.Fatalf("%v %+v", objs, st)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFile(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := OpenFile(dir); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory: %v", err)
	}
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(junk); !errors.Is(err, ErrNotHxStore) {
		t.Fatal(err)
	}

	orig := statFile
	defer func() { statFile = orig }()
	statFile = func(*os.File) (os.FileInfo, error) { return nil, errBoom }
	if _, err := OpenFile(path); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}

// TestWalkFindsObjectsAcrossBlocks: offsets are file offsets of the block, not
// page aligned, and objects come out in file order.
func TestWalkFindsObjectsAcrossBlocks(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Pad(3) // nothing here is aligned to anything
	first := hxbuild.NewObject(0x6b, 24, 24)
	first.PutU32(12, 0xdeadbeef)
	b.Block(hxbuild.Payload(first, hxbuild.NewObject(0x6e, 16, 16)))
	secondAt := int64(b.Len())
	b.Block(hxbuild.Payload(hxbuild.NewObject(0x71, 12, 12)))
	s := mustOpen(t, b.Bytes())
	objs, st := walkAll(t, s)
	if len(objs) != 3 {
		t.Fatalf("%+v", objs)
	}
	if objs[0].block != int64(hxbuild.FileHeaderSize+3) || objs[0].pos != 0 || objs[0].class != 0x6b || objs[0].tag != 24 {
		t.Fatalf("%+v", objs[0])
	}
	if objs[1].pos != 4+24 || objs[1].class != 0x6e {
		t.Fatalf("%+v", objs[1])
	}
	if objs[2].block != secondAt || objs[2].class != 0x71 {
		t.Fatalf("%+v", objs[2])
	}
	if got := binary.LittleEndian.Uint32(objs[0].raw[12:]); got != 0xdeadbeef {
		t.Fatalf("%#x", got)
	}
	if len(objs[0].raw) != 24 || objs[0].raw[0] != 5 {
		t.Fatalf("raw must start at the envelope and be L bytes: %v", objs[0].raw)
	}
	wantBytes := int64(4 + 24 + 4 + 16 + 4 + 12)
	if st.BlocksFound != 2 || st.BlocksValid != 2 || st.BlocksRejected() != 0 || st.Objects != 3 ||
		st.PayloadBytes != wantBytes || st.UnwalkedBytes != 0 || st.PairsOverflow != 0 {
		t.Fatalf("%+v", st)
	}
	if st.Pairs[Pair{0x6b, 24}] != 1 || len(st.Pairs) != 3 {
		t.Fatalf("%v", st.Pairs)
	}
}

func TestWalkCountsPairsAndUnwalked(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	p := hxbuild.Payload(hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(1, 13, 13))
	p = append(p, 1, 2, 3, 4, 5, 6, 7) // not an object
	b.Block(p)
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.Pairs[Pair{1, 12}] != 2 || st.Pairs[Pair{1, 13}] != 1 || st.Objects != 3 || st.UnwalkedBytes != 7 {
		t.Fatalf("%+v", st)
	}
}

func TestWalkPairsOverflow(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	var objs []*hxbuild.Object
	for c := 0; c < maxPairs+10; c++ {
		objs = append(objs, hxbuild.NewObject(uint16(c), 12, 12))
	}
	objs = append(objs, hxbuild.NewObject(0, 12, 12)) // a known pair is still counted past the cap
	b.Block(hxbuild.Payload(objs...))
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if len(st.Pairs) != maxPairs || st.PairsOverflow != 10 || st.Objects != maxPairs+11 || st.Pairs[Pair{0, 12}] != 2 {
		t.Fatalf("pairs %d overflow %d objects %d", len(st.Pairs), st.PairsOverflow, st.Objects)
	}
}

func goodBlock() []byte {
	return hxbuild.EncodeBlock(8, hxbuild.Payload(hxbuild.NewObject(9, 12, 12)))
}

// TestScanSkipsInvalidAndContinues: every kind of bad candidate is counted by
// reason and the good blocks around it are all still read.
func TestScanSkipsInvalidAndContinues(t *testing.T) {
	mut := func(f func(b []byte)) []byte { b := goodBlock(); f(b); return b }
	bad := map[string][]byte{
		RejectHeaderCRC:     mut(func(b []byte) { b[0] ^= 1 }),
		RejectPayloadCRC:    mut(func(b []byte) { b[len(b)-1] ^= 1 }),
		RejectHeaderUnknown: mut(func(b []byte) { put32(b, offConstant, 5); fix(b) }),
		RejectTypeOther:     mut(func(b []byte) { put32(b, offType, 16); fix(b) }),
		RejectOversize:      mut(func(b []byte) { put32(b, offInflated, MaxInflated+1); fix(b) }),
		RejectInflate:       mut(func(b []byte) { put32(b, offInflated, 3); fix(b) }),
	}
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(goodBlock())
	for _, blk := range bad {
		b.Raw(blk)
		b.Raw(goodBlock())
	}
	b.Raw(goodBlock())
	b.Raw(goodBlock()[:HeaderSize+2]) // torn at the end of the file
	st := func() Stats { _, st := walkAll(t, mustOpen(t, b.Bytes())); return st }()
	if st.BlocksValid != 2+len(bad) {
		t.Fatalf("%+v", st)
	}
	for reason := range bad {
		if st.Rejected[reason] != 1 {
			t.Fatalf("reason %s: %v", reason, st.Rejected)
		}
	}
	if st.Rejected[RejectTruncated] != 1 || st.BlocksRejected() != len(bad)+1 || st.BlocksFound != st.BlocksValid+st.BlocksRejected() {
		t.Fatalf("%+v", st)
	}
	if st.Objects != st.BlocksValid {
		t.Fatalf("objects %d", st.Objects)
	}
}

// TestScanMagicNearEndOfFile: a magic with fewer than a header's worth of bytes
// before the end of the file is counted as truncated.
func TestScanMagicNearEndOfFile(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(goodBlock()[:offMagic+8+3])
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.BlocksFound != 1 || st.Rejected[RejectTruncated] != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestScanTruncatedAndOversizePayloadLengths(t *testing.T) {
	// A header that declares more payload than the file holds, and one that
	// declares more than any valid block can have, with a good block after each.
	long := goodBlock()
	put32(long, offPayloadLen, binary.LittleEndian.Uint32(long[offPayloadLen:])+1) // one past what exists
	fix(long)
	long = long[:len(long)-1]
	huge := goodBlock()
	put32(huge, offPayloadLen, maxPayload+1)
	fix(huge)
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(huge)
	b.Raw(goodBlock())
	b.Raw(goodBlock())
	b.Raw(long)
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.BlocksValid != 2 || st.Rejected[RejectTruncated] != 1 || st.Rejected[RejectOversize] != 1 {
		t.Fatalf("%+v", st)
	}
}

// TestScanZeroLengthPayload: a block that declares an empty payload is read as
// far as the header and then fails the inflate check, not a read.
func TestScanZeroLengthPayload(t *testing.T) {
	blk := goodBlock()[:HeaderSize]
	put32(blk, offPayloadLen, 0)
	fix(blk)
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(blk)
	b.Raw(goodBlock())
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.BlocksValid != 1 || st.Rejected[RejectInflate] != 1 {
		t.Fatalf("%+v", st)
	}
}

// TestScanDoesNotOverlapValidBlocks: a whole valid block sits inside another
// block's literal payload. The scan resumes after the outer block, so the inner
// one is not a second block.
func TestScanDoesNotOverlapValidBlocks(t *testing.T) {
	inner := hxbuild.EncodeBlock(8, []byte("FIXTURE inner block"))
	outerPayload := hxbuild.Payload(hxbuild.NewObject(1, 12, 12))
	outerPayload = append(outerPayload, inner...)
	b := hxbuild.New(hxbuild.Options{})
	b.Block(outerPayload)
	b.Raw(goodBlock())
	objs, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.BlocksFound != 2 || st.BlocksValid != 2 || len(objs) != 2 {
		t.Fatalf("%+v %v", st, objs)
	}
	if st.UnwalkedBytes != int64(len(inner)) {
		t.Fatalf("the embedded bytes are unwalked payload: %d", st.UnwalkedBytes)
	}
}

// TestScanRejectedCandidateResumesPastMagic: a block whose header is torn but
// which has a good block hidden in its bytes still yields the hidden block,
// because the scan resumes just past the bad magic.
func TestScanRejectedCandidateResumesPastMagic(t *testing.T) {
	torn := goodBlock()
	torn[0] ^= 1
	outer := append(append([]byte(nil), torn[:HeaderSize]...), goodBlock()...)
	b := hxbuild.New(hxbuild.Options{})
	b.Raw(outer)
	_, st := walkAll(t, mustOpen(t, b.Bytes()))
	if st.BlocksFound != 2 || st.BlocksValid != 1 || st.Rejected[RejectHeaderCRC] != 1 {
		t.Fatalf("%+v", st)
	}
}

// TestScanFindsBlocksAcrossWindowBoundary puts the magic across the 1 MiB
// window edge at every alignment from one byte in to seven, and far into a
// later window.
func TestScanFindsBlocksAcrossWindowBoundary(t *testing.T) {
	for cut := 1; cut < len(blockMagic); cut++ {
		b := hxbuild.New(hxbuild.Options{})
		// The magic starts at block start + 8; place it so that "cut" of its
		// bytes lie before the window edge.
		b.Pad(scanWindow - cut - offMagic - hxbuild.FileHeaderSize)
		b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
		_, st := walkAll(t, mustOpen(t, b.Bytes()))
		if st.BlocksFound != 1 || st.BlocksValid != 1 {
			t.Fatalf("cut %d: %+v", cut, st)
		}
	}
	// Several windows of nothing, then blocks at odd offsets in later windows.
	b := hxbuild.New(hxbuild.Options{})
	b.Pad(2*scanWindow + 17)
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	b.Pad(scanWindow + 5)
	b.Block(hxbuild.Payload(hxbuild.NewObject(2, 12, 12)))
	b.Pad(7) // a tail shorter than a magic
	objs, st := walkAll(t, mustOpen(t, b.Bytes()))
	if len(objs) != 2 || st.BlocksValid != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestScanPageAlignedAndNot(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.AlignPage()
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	b.AlignPage()
	b.AlignPage() // already aligned: nothing more
	b.Block(hxbuild.Payload(hxbuild.NewObject(2, 12, 12)))
	b.Pad(1)
	b.Block(hxbuild.Payload(hxbuild.NewObject(3, 12, 12)))
	objs, _ := walkAll(t, mustOpen(t, b.Bytes()))
	if len(objs) != 3 || objs[0].block%4096 != 0 || objs[2].block%4096 == 0 {
		t.Fatalf("%+v", objs)
	}
}

func TestWalkNoBlocks(t *testing.T) {
	for _, pad := range []int{0, 3, 8, 100} {
		b := hxbuild.New(hxbuild.Options{})
		b.Pad(pad)
		objs, st := walkAll(t, mustOpen(t, b.Bytes()))
		if len(objs) != 0 || st.BlocksFound != 0 {
			t.Fatalf("pad %d: %+v", pad, st)
		}
	}
}

func TestWalkContextCancel(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	b.Block(hxbuild.Payload(hxbuild.NewObject(2, 12, 12)))
	s := mustOpen(t, b.Bytes())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Walk(ctx, WalkOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before the first window: %v", err)
	}

	// Cancelled while a block is being walked: the next block is not read.
	ctx, cancel = context.WithCancel(context.Background())
	calls := 0
	st, err := s.Walk(ctx, WalkOptions{}, func(Object) error { calls++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || calls != 1 || st.BlocksValid != 1 {
		t.Fatal(err, calls, st)
	}
}

// ctxFlip reports no error for the first n calls to Err and then cancels.
type ctxFlip struct {
	context.Context
	n int
}

func (c *ctxFlip) Err() error {
	if c.n > 0 {
		c.n--
		return nil
	}
	return context.Canceled
}

func TestWalkContextCancelAfterScan(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	s := mustOpen(t, b.Bytes())
	// Err is consulted in the window fill (call 1) and then once per block (call 2).
	if _, err := s.Walk(&ctxFlip{Context: context.Background(), n: 1}, WalkOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWalkReadErrorPropagates(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12)))
	data := b.Bytes()
	blockAt := int64(hxbuild.FileHeaderSize)
	fileLen := int64(len(data))
	cases := map[string]failReader{
		"window error": {when: func(off int64, n int) bool {
			return off == 0 && n > hxbuild.FileHeaderSize || n == int(fileLen)-hxbuild.FileHeaderSize && off == 0
		}},
		"window short": {when: func(off int64, n int) bool { return off == 0 && n > hxbuild.FileHeaderSize }, short: true},
		"window eof":   {when: func(off int64, n int) bool { return off == 0 && n > hxbuild.FileHeaderSize }, eof: true},
		"header error": {when: func(off int64, n int) bool { return off == blockAt && n == HeaderSize }},
		"body error":   {when: func(off int64, n int) bool { return off == blockAt+HeaderSize }},
		"body short":   {when: func(off int64, n int) bool { return off == blockAt+HeaderSize }, short: true},
		"body eof":     {when: func(off int64, n int) bool { return off == blockAt+HeaderSize }, eof: true},
	}
	for name, fr := range cases {
		t.Run(name, func(t *testing.T) {
			fr.r = bytes.NewReader(data)
			s, err := Open(fr, fileLen)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Walk(context.Background(), WalkOptions{}, nil)
			if err == nil || errors.Is(err, context.Canceled) || err.Error() == "" {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestWalkCallbackErrorStops(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(2, 12, 12)))
	b.Block(hxbuild.Payload(hxbuild.NewObject(3, 12, 12)))
	calls := 0
	st, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(Object) error { calls++; return errBoom })
	if !errors.Is(err, errBoom) || calls != 1 || st.BlocksValid != 1 {
		t.Fatal(err, calls, st)
	}
}

// TestWalkBuffersGrowAndAreReused: blocks of growing and then shrinking size
// are all read correctly, and the object bytes handed to the callback are valid
// during it.
func TestWalkBuffersGrowAndAreReused(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	for _, size := range []int{50, 5000, 200, 70000, 100} {
		o := hxbuild.NewObject(uint16(size%1000&0xffff), 12, 12)
		o.Append(bytes.Repeat([]byte{byte(size & 0xff)}, size))
		b.Block(hxbuild.Payload(o))
	}
	n := 0
	_, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(o Object) error {
		n++
		if last := o.Raw[len(o.Raw)-1]; int(last) != len(o.Raw)-12 && int(last) != (len(o.Raw)-12)&0xff {
			t.Errorf("object %d holds the wrong bytes", n)
		}
		return nil
	})
	if err != nil || n != 5 {
		t.Fatal(err, n)
	}
}

func TestWalkMaxInflatedOption(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	o := hxbuild.NewObject(1, 12, 12)
	o.Append(make([]byte, 1000))
	b.Block(hxbuild.Payload(o))
	s := mustOpen(t, b.Bytes())
	st, err := s.Walk(context.Background(), WalkOptions{MaxInflated: 100}, nil)
	if err != nil || st.Rejected[RejectOversize] != 1 || st.BlocksValid != 0 {
		t.Fatal(err, st)
	}
	for _, limit := range []int{0, -5, MaxInflated + 1} {
		st, err = s.Walk(context.Background(), WalkOptions{MaxInflated: limit}, nil)
		if err != nil || st.BlocksValid != 1 {
			t.Fatalf("limit %d: %v %+v", limit, err, st)
		}
	}
}

// TestWalkLargestBlockStaysWithinTheCap: a block at the cap inflates, one byte
// more is rejected before any buffer for it is made.
func TestWalkLargestBlockStaysWithinTheCap(t *testing.T) {
	big := make([]byte, MaxInflated)
	b := hxbuild.New(hxbuild.Options{})
	b.Block(big)
	st, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, nil)
	if err != nil || st.BlocksValid != 1 || st.PayloadBytes != MaxInflated || st.UnwalkedBytes != MaxInflated {
		t.Fatal(err, st)
	}
}

func TestStatsBlocksRejectedEmpty(t *testing.T) {
	if (Stats{}).BlocksRejected() != 0 {
		t.Fatal("empty stats")
	}
}
