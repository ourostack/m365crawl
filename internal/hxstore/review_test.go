package hxstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

func TestOpenFileChecksTheOpenedHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store")
	if err := os.WriteFile(path, hxbuild.New(hxbuild.Options{}).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirInfo, _ := os.Stat(dir)
	otherInfo, _ := os.Stat(other)
	origPath, origFile := statPath, statFile
	defer func() { statPath, statFile = origPath, origFile }()

	// The path was checked as a different file than the one that got opened.
	statPath = func(string) (fs.FileInfo, error) { return otherInfo, nil }
	if _, err := OpenFile(path); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("swapped file: %v", err)
	}
	// The opened handle is not a regular file.
	statPath, statFile = origPath, func(*os.File) (fs.FileInfo, error) { return dirInfo, nil }
	if _, err := OpenFile(path); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("handle not regular: %v", err)
	}
	// A path that cannot be opened after a good stat (no read permission).
	statPath = func(string) (fs.FileInfo, error) { return otherInfo, nil }
	if _, err := OpenFile(filepath.Join(dir, "gone")); err == nil || errors.Is(err, ErrNotRegular) {
		t.Fatalf("open failure: %v", err)
	}
}

func TestClassifyMagic(t *testing.T) {
	if r, soft := classify(ErrBlockMagic); r != RejectMagic || !soft {
		t.Fatal(r, soft)
	}
	if _, soft := classify(errBoom); soft {
		t.Fatal("unknown errors are not block rejections")
	}
}

// TestResyncFlag: contiguous objects are not resynced; the first object after
// skipped bytes is, and the flag clears at the next contiguous object. A corrupt
// outer length around a well-formed inner envelope reports the inner one as
// resynced.
func TestResyncFlag(t *testing.T) {
	a, b, c := hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(2, 12, 12), hxbuild.NewObject(3, 12, 12)
	var p []byte
	p = append(p, a.Encode()...)                            // contiguous from the payload start
	p = append(p, b.Encode()...)                            // contiguous
	p = append(p, 0xde, 0xad)                               // skipped bytes
	p = append(p, c.Encode()...)                            // resynced
	p = append(p, hxbuild.NewObject(4, 12, 12).Encode()...) // contiguous again
	var got []bool
	var classes []uint16
	walkObjects(p, func(_ int, class, _ uint16, _ []byte, r bool) error {
		got = append(got, r)
		classes = append(classes, class)
		return nil
	})
	want := []bool{false, false, true, false}
	if len(got) != 4 {
		t.Fatalf("%v %v", got, classes)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("flags %v, want %v", got, want)
		}
	}

	// A malformed outer object (length too long for the inner envelope's
	// repeat) around a good inner one.
	inner := hxbuild.NewObject(9, 12, 12).Encode()
	outer := append([]byte{0x20, 0, 0, 0, 5, 0, 0x20, 0, 0x21, 0, 0, 0, 0, 0, 7, 0}, inner...)
	got = nil
	walkObjects(outer, func(_ int, class, _ uint16, _ []byte, r bool) error {
		if class != 9 {
			t.Fatalf("class %d", class)
		}
		got = append(got, r)
		return nil
	})
	if len(got) != 1 || !got[0] {
		t.Fatalf("inner envelope must be flagged resynced: %v", got)
	}
}

func TestResyncStats(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	p := hxbuild.Payload(hxbuild.NewObject(1, 12, 12))
	p = append(p, 0xff)
	p = append(p, hxbuild.Payload(hxbuild.NewObject(1, 12, 12), hxbuild.NewObject(2, 12, 12))...)
	b.Block(p)
	var flags []bool
	st, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(o Object) error {
		flags = append(flags, o.Resynced)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Objects != 3 || st.ObjectsResynced != 1 || st.PairsResynced[Pair{1, 12}] != 1 || len(st.PairsResynced) != 1 ||
		flags[0] || !flags[1] || flags[2] {
		t.Fatalf("%+v %v", st, flags)
	}
}

// TestResyncStatsPastPairCap: an object whose pair is over the cap is counted as
// resynced overall but has no per-pair entry.
func TestResyncStatsPastPairCap(t *testing.T) {
	var st Stats
	for c := 0; c < maxPairs; c++ {
		st.countPair(Pair{uint16(c), 12}, false)
	}
	st.countPair(Pair{60000, 12}, true)
	if st.ObjectsResynced != 1 || st.PairsOverflow != 1 || len(st.PairsResynced) != 0 {
		t.Fatalf("%+v", st.ObjectsResynced)
	}
}

// TestRawAliasesTheReaderBuffer pins the documented behaviour: a slice kept
// after the callback changes when the next block is read, and Clone does not.
func TestRawAliasesTheReaderBuffer(t *testing.T) {
	mk := func(fill byte) []byte {
		o := hxbuild.NewObject(1, 12, 12)
		o.Append(bytes.Repeat([]byte{fill}, 8))
		return hxbuild.Payload(o)
	}
	b := hxbuild.New(hxbuild.Options{})
	b.Block(mk('A'))
	b.Block(mk('B'))
	var kept, cloned Object
	n := 0
	_, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(o Object) error {
		if n == 0 {
			kept, cloned = o, o.Clone()
		}
		n++
		return nil
	})
	if err != nil || n != 2 {
		t.Fatal(err, n)
	}
	if kept.Raw[len(kept.Raw)-1] != 'B' {
		t.Fatalf("a kept Raw must alias the reused buffer, last byte %q", kept.Raw[len(kept.Raw)-1])
	}
	if cloned.Raw[len(cloned.Raw)-1] != 'A' || cloned.Class != 1 || cloned.Len() != 20 {
		t.Fatalf("Clone must be independent: %+v", cloned)
	}
}

// The fixture below was computed outside this repository's code (Python's
// struct and zlib.crc32, from the format description), not by hxbuild. The
// payload is synthetic: one object holding the ASCII text FIXT. No bytes of a
// real store are in it.
const (
	// u32 length 16, then the envelope: u16 5, u16 tag 16, u32 length 16, u16 0,
	// u16 class 0x6b, then four body bytes "FIXT".
	pinnedObject = "10000000050010001000000000006b0046495854"
	// A block of type 8: header CRC, payload CRC, magic, type 8, payload length
	// 22, inflated length 20, constant 4, eight zero bytes, then the LZ4 payload:
	// token 0xf0, extension 5 (20 literals), the object above.
	pinnedBlock = "9298d035c9b179fd056a703b6445025d080000001600000014000000040000000000000000000000f00510000000050010001000000000006b0046495854"
	// File header: "Nostromo", version 'i', page size 4096 at +0x38.
	pinnedFileHeader = "4e6f7374726f6d6f6900000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000010000000000000"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPinnedBytes(t *testing.T) {
	obj, blk, hdr := unhex(t, pinnedObject), unhex(t, pinnedBlock), unhex(t, pinnedFileHeader)
	// The builder agrees with the independently computed bytes.
	o := hxbuild.NewObject(0x6b, 16, 16)
	o.PutU32(12, 0x54584946) // "FIXT"
	if !bytes.Equal(o.Encode(), obj) {
		t.Fatalf("builder object %x", o.Encode())
	}
	if !bytes.Equal(hxbuild.EncodeBlock(8, obj), blk) {
		t.Fatalf("builder block %x", hxbuild.EncodeBlock(8, obj))
	}
	if !bytes.Equal(hxbuild.New(hxbuild.Options{}).Bytes(), hdr) {
		t.Fatal("builder header")
	}
	// The reader accepts them as they are.
	_, out, err := VerifyBlock(blk, nil, MaxInflated)
	if err != nil || !bytes.Equal(out, obj) {
		t.Fatalf("%v %x", err, out)
	}
	file := append(append([]byte(nil), hdr...), blk...)
	var got Object
	st, err := mustOpen(t, file).Walk(context.Background(), WalkOptions{}, func(o Object) error { got = o.Clone(); return nil })
	if err != nil || st.BlocksValid != 1 || got.Class != 0x6b || got.Tag != 16 || got.Resynced ||
		got.BlockOffset != 0x40 || !bytes.Equal(got.Raw, obj[4:]) {
		t.Fatalf("%v %+v %+v", err, st, got)
	}
}

// TestRoundTrip: objects built with hxbuild come back from Walk with the same
// class, tag, length and bytes, in order, none marked resynced.
func TestRoundTrip(t *testing.T) {
	type want struct {
		class, tag uint16
		raw        []byte
	}
	var wants []want
	b := hxbuild.New(hxbuild.Options{})
	for blk := 0; blk < 4; blk++ {
		var objs []*hxbuild.Object
		for i := 0; i < 6; i++ {
			size := 12 + (blk*7+i*13)%90
			o := hxbuild.NewObject(uint16(0x40+blk*8+i), uint16(size&0xffff), size)
			for k := 12; k < size; k++ {
				o.PutU8(k, byte((k*31+i+blk)&0xff))
			}
			if i%2 == 0 {
				o.AppendString("Alex Fixture")
			}
			objs = append(objs, o)
			wants = append(wants, want{uint16(0x40 + blk*8 + i), uint16(size&0xffff), o.Encode()[4:]})
		}
		b.Pad(blk * 3)
		b.Block(hxbuild.Payload(objs...))
	}
	i := 0
	st, err := mustOpen(t, b.Bytes()).Walk(context.Background(), WalkOptions{}, func(o Object) error {
		w := wants[i]
		if o.Class != w.class || o.Tag != w.tag || o.Len() != len(w.raw) || !bytes.Equal(o.Raw, w.raw) || o.Resynced {
			t.Fatalf("object %d differs: %+v", i, o)
		}
		i++
		return nil
	})
	if err != nil || i != len(wants) || st.ObjectsResynced != 0 || st.UnwalkedBytes != 0 || len(st.PairsResynced) != 0 {
		t.Fatal(err, i, st)
	}
}
