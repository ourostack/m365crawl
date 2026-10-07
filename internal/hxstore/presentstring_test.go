package hxstore

import (
	"encoding/binary"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// psObject lays out a 40-byte fixed region (word at 8, length at 12) followed by a string area at
// base 40, holding the given raw bytes.
func psObject(off, length uint32, area []byte) Object {
	raw := make([]byte, 40, 40+len(area))
	binary.LittleEndian.PutUint32(raw[8:], off)
	binary.LittleEndian.PutUint32(raw[12:], length)
	return Object{Raw: append(raw, area...)}
}

func TestPresentString(t *testing.T) {
	text := hxbuild.UTF16Z("fixture") // 16 bytes with the terminator
	surrogate := []byte{0x00, 0xd8, 0, 0}
	cases := []struct {
		name        string
		o           Object
		wordOff     int
		base        int
		want        string
		present, ok bool
	}{
		{"present", psObject(0, 16, text), 8, 40, "fixture", true, true},
		{"present at an odd offset", psObject(1, 16, append([]byte{0}, text...)), 8, 40, "fixture", true, true},
		{"bit 31 masked", psObject(0, 16|1<<31, text), 8, 40, "fixture", true, true},
		{"absent: length 0", psObject(0, 0, text), 8, 40, "", false, true},
		{"absent: length 0 with only bit 31", psObject(0, 1<<31, text), 8, 40, "", false, true},
		{"odd length", psObject(0, 15, text), 8, 40, "", true, false},
		{"length too short for a terminator", psObject(0, 1, text), 8, 40, "", true, false},
		{"runs past the object", psObject(0, 18, text), 8, 40, "", true, false},
		{"offset past the object", psObject(1000, 16, text), 8, 40, "", true, false},
		{"no terminator at the stated end", psObject(0, 14, text), 8, 40, "", true, false},
		{"terminator before the stated end", psObject(0, 8, hxbuild.UTF16Z("a\x00b")), 8, 40, "", true, false},
		{"unpaired surrogate", psObject(0, 4, surrogate), 8, 40, "", true, false},
		{"negative base", psObject(0, 16, text), 8, -1, "", true, false},
		{"word outside the object", psObject(0, 16, text), 1000, 40, "", false, false},
		{"length word outside the object", Object{Raw: make([]byte, 10)}, 8, 40, "", false, false},
		{"huge length", psObject(0, 0x7fffffff, text), 8, 40, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, present, ok := c.o.PresentString(c.wordOff, c.base)
			if s != c.want || present != c.present || ok != c.ok {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", s, present, ok, c.want, c.present, c.ok)
			}
		})
	}
}
