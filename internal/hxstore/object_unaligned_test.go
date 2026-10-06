package hxstore

import (
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

// TestStringAtUnaligned reads text that starts at an odd offset, the way the
// calendar objects hold it, and checks that StringAt still refuses the same bytes.
func TestStringAtUnaligned(t *testing.T) {
	o := hxbuild.NewObject(1, 13, 13) // 13 bytes: the next string starts at an odd offset
	at := o.AppendString("Odd Fixture \U0001F600")
	if at%2 != 1 {
		t.Fatal("test needs an odd start")
	}
	ob := obj(o.Encode()[4:])
	if s, ok := ob.StringAtUnaligned(at); !ok || s != "Odd Fixture \U0001F600" {
		t.Fatal(s, ok)
	}
	if _, ok := ob.StringAt(at); ok {
		t.Fatal("StringAt must keep refusing odd starts")
	}
	// An even start works the same through both.
	if s, ok := obj([]byte{'a', 0, 0, 0}).StringAtUnaligned(0); !ok || s != "a" {
		t.Fatal(s, ok)
	}
}

func TestStringAtUnalignedInvalid(t *testing.T) {
	nul := []byte{0, 0}
	for name, tc := range map[string]struct {
		raw []byte
		off int
	}{
		"negative":         {[]byte{'a', 0, 0, 0}, -1},
		"at end":           {[]byte{'a', 0, 0, 0}, 4},
		"no terminator":    {[]byte{0, 'a', 0, 'b', 0}, 1},
		"half terminator":  {[]byte{0, 'a', 0, 0}, 1},
		"lone surrogate":   {append([]byte{0, 0x00, 0xdc}, nul...), 1},
		"high then letter": {append([]byte{0, 0x00, 0xd8, 'a', 0}, nul...), 1},
		"high at end":      {append([]byte{0, 0x00, 0xd8}, nul...), 1},
	} {
		if s, ok := obj(tc.raw).StringAtUnaligned(tc.off); ok {
			t.Errorf("%s: got %q", name, s)
		}
	}
}

func TestStringUnaligned(t *testing.T) {
	o := hxbuild.NewObject(1, 45, 45)
	o.PutStringWord(8, 45, "Word Fixture")
	ob := obj(o.Encode()[4:])
	if s, ok := ob.StringUnaligned(8, 45); !ok || s != "Word Fixture" {
		t.Fatal(s, ok)
	}
	for _, c := range []struct{ word, base int }{{8, 1000}, {8, -1}, {200, 0}} {
		if _, ok := ob.StringUnaligned(c.word, c.base); ok {
			t.Fatalf("%+v", c)
		}
	}
}

func TestBytes(t *testing.T) {
	o := obj([]byte{1, 2, 3, 4})
	if b, ok := o.Bytes(1, 2); !ok || len(b) != 2 || b[0] != 2 {
		t.Fatal(b, ok)
	}
	if b, ok := o.Bytes(4, 0); !ok || len(b) != 0 {
		t.Fatal("empty at the end is inside")
	}
	for _, c := range [][2]int{{3, 2}, {-1, 1}, {0, -1}, {5, 0}} {
		if _, ok := o.Bytes(c[0], c[1]); ok {
			t.Fatalf("%v", c)
		}
	}
}
