package hxstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// FuzzWalkObjects feeds arbitrary bytes to the object walk and checks what the
// walk promises: nothing panics, every object lies inside the payload and starts
// with a well-formed envelope, objects come in order without overlap, and the
// covered count is the sum of what was reported.
func FuzzWalkObjects(f *testing.F) {
	f.Add(hxbuild.Payload(hxbuild.NewObject(0x6b, 24, 24), hxbuild.NewObject(0x6e, 12, 12)))
	f.Add(append([]byte{1, 2, 3}, hxbuild.Payload(hxbuild.NewObject(1, 40, 40))...))
	f.Add(envelope(0xffffffff, 5, 20, 0xffffffff, 0, 7))
	f.Add(envelope(20, 5, 20, 21, 0, 7))
	f.Add(envelope(11, 5, 8, 11, 0, 7))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) {
		next, covered := 0, 0
		st := &Stats{}
		cov, err := walkObjects(p, st, func(pos int, class, tag uint16, raw []byte, _ bool) error {
			end := pos + lenPrefix + len(raw)
			if pos < next || end > len(p) || len(raw) < EnvelopeSize || int(tag) > len(raw) {
				t.Fatalf("object at %d len %d tag %d, previous end %d, payload %d", pos, len(raw), tag, next, len(p))
			}
			if int(binary.LittleEndian.Uint32(p[pos:])) != len(raw) || raw[0] != 5 || raw[1] != 0 ||
				int(binary.LittleEndian.Uint32(raw[4:])) != len(raw) || raw[8] != 0 || raw[9] != 0 ||
				binary.LittleEndian.Uint16(raw[10:]) != class || binary.LittleEndian.Uint16(raw[2:]) != tag {
				t.Fatalf("envelope at %d is not the documented one", pos)
			}
			next = end
			covered += lenPrefix + len(raw)
			return nil
		})
		if st.FramingBytes < 0 || cov+int(st.FramingBytes) > len(p) {
			t.Fatalf("object bytes %d + framing %d exceed payload %d", cov, st.FramingBytes, len(p))
		}
		if err != nil || cov != covered || cov > len(p) {
			t.Fatalf("covered %d, summed %d, payload %d, err %v", cov, covered, len(p), err)
		}
	})
}

// FuzzObjectAccess drives every accessor, including the string reader, with
// arbitrary bytes and offsets. None may panic; a string that is returned must be
// valid UTF-8 and must have come from inside the object.
func FuzzObjectAccess(f *testing.F) {
	o := hxbuild.NewObject(0x6b, 40, 40)
	o.PutStringWord(12, 20, "Subject Fixture")
	o.PutU64(24, 637_000_000_000_000_000)
	f.Add(o.Encode()[4:], int32(12), int32(20))
	f.Add(hxbuild.UTF16Z("Alex Fixture \U0001F600"), int32(0), int32(0))
	f.Add([]byte{0x00, 0xd8, 0, 0}, int32(0), int32(0))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, int32(0), int32(-1))
	f.Add([]byte{}, int32(0), int32(0))
	f.Fuzz(func(t *testing.T, raw []byte, off, base int32) {
		ob := Object{Raw: raw}
		o, b := int(off), int(base)
		ob.U8(o)
		ob.U16(o)
		ob.U32(o)
		ob.U64(o)
		if tm, ok := ob.Ticks(o); ok && (tm.Year() < 1900 || tm.Year() >= 2200 || tm.Location() != time.UTC) {
			t.Fatalf("ticks out of range: %v", tm)
		}
		for _, s := range []func() (string, bool){
			func() (string, bool) { return ob.StringAt(o) },
			func() (string, bool) { return ob.String(o, b) },
		} {
			if str, ok := s(); ok {
				if !utf8.ValidString(str) || len(str) > 3*len(raw)/2+3 {
					t.Fatalf("bad string result of %d bytes from %d", len(str), len(raw))
				}
			} else if str != "" {
				t.Fatalf("string with ok false: %q", str)
			}
		}
	})
}

// FuzzWalk gives whole files to Open and Walk. They may reject anything but must
// not panic, must account for every block they find, and must stay within the
// inflate limit.
func FuzzWalk(f *testing.F) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.Payload(hxbuild.NewObject(0x6b, 24, 24)))
	b.Pad(5)
	b.Raw(hxbuild.EncodeBlock(16, []byte("other")))
	torn := hxbuild.EncodeBlock(8, []byte("torn"))
	torn[3] ^= 1
	b.Raw(torn)
	f.Add(b.Bytes())
	f.Add(b.Bytes()[:b.Len()-3])
	f.Add(hxbuild.New(hxbuild.Options{}).Bytes())
	f.Add(hxbuild.New(hxbuild.Options{Version: 'x'}).Bytes())
	f.Add([]byte("Nostromo"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		var objectBytes int64
		st, err := s.Walk(context.Background(), WalkOptions{MaxInflated: fuzzLimit}, func(o Object) error {
			objectBytes += int64(lenPrefix + len(o.Raw))
			if len(o.Raw) > fuzzLimit || o.BlockOffset < 0 || o.BlockOffset >= int64(len(data)) {
				t.Fatalf("object %d bytes at block %d", len(o.Raw), o.BlockOffset)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if objectBytes+st.FramingBytes+st.UnwalkedBytes != st.PayloadBytes {
			t.Fatalf("accounting: objects %d + framing %d + unwalked %d != payload %d", objectBytes, st.FramingBytes, st.UnwalkedBytes, st.PayloadBytes)
		}
		if st.BlocksFound != st.BlocksValid+st.BlocksRejected() || st.UnwalkedBytes+st.FramingBytes > st.PayloadBytes || st.UnwalkedBytes < 0 || st.FramingBytes < 0 {
			t.Fatalf("inconsistent stats %+v", st)
		}
	})
}
