package hxbuild

import "testing"

func TestNewAccount(t *testing.T) {
	o := NewAccount(AccountSpec{Address: "Fixture.User@example.com", Lead: 6, Text: "Fixture text"})
	b := o.Encode()[4:]
	if u16(b, 2) != TagAccount || u16(b, 10) != ClassAccount || len(b) != int(u32(b, 4)) || u32(b, acAreaOne) != 6 {
		t.Fatalf("envelope: tag %x class %x", u16(b, 2), u16(b, 10))
	}
	base := AccountSize + 6
	for _, w := range []int{acAddress, acAddress2} {
		if got := utf16z(t, b, base+int(u32(b, w))); got != "Fixture.User@example.com" {
			t.Fatalf("word %d reads %q", w, got)
		}
		if u32(b, w+4) != uint32(2*len("Fixture.User@example.com")+2) {
			t.Fatalf("length word %d", u32(b, w+4))
		}
	}
	if got := utf16z(t, b, base); got != "Fixture text" {
		t.Fatalf("string area starts with %q", got)
	}
}

func TestNewAccountAbsentAndOverrides(t *testing.T) {
	o := NewAccount(AccountSpec{Tag: 0x19d1, Text: "Fixture text"})
	b := o.Encode()[4:]
	if u16(b, 2) != 0x19d1 || u32(b, acAddress) != 0 || u32(b, acAddress+4) != 0 {
		t.Fatal("an account without an address must carry absent strings")
	}
	o = NewAccount(AccountSpec{Address: "a@example.com", LengthBytes: 7})
	if u32(o.Encode()[4:], acAddress+4) != 7 {
		t.Fatal("LengthBytes did not replace the length word")
	}
	o = NewAccount(AccountSpec{Address: "a@example.com", SecondAddress: "b@example.com"})
	b = o.Encode()[4:]
	if utf16z(t, b, AccountSize+int(u32(b, acAddress2))) != "b@example.com" || utf16z(t, b, AccountSize+int(u32(b, acAddress))) != "a@example.com" {
		t.Fatal("the second copy was not written apart")
	}
}
