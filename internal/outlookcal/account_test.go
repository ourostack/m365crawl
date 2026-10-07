package outlookcal

import (
	"testing"

	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

// accountObject builds an account object and reads it back through the reader, as a store would give it.
func accountObject(t *testing.T, spec hxbuild.AccountSpec) hxstore.Object {
	t.Helper()
	var got hxstore.Object
	s := storeOf(t, framed(hxbuild.NewAccount(spec)))
	if _, err := s.Walk(t.Context(), hxstore.WalkOptions{}, func(o hxstore.Object) error { got = o.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAccountAddress(t *testing.T) {
	cases := []struct {
		name string
		spec hxbuild.AccountSpec
		want string
	}{
		{"read and lower-cased", hxbuild.AccountSpec{Address: "Fixture.User@Example.COM"}, "fixture.user@example.com"},
		{"with area one", hxbuild.AccountSpec{Address: "a@example.com", Lead: 7, Text: "Fixture text"}, "a@example.com"},
		{"absent address reads the first string, so it is refused", hxbuild.AccountSpec{Text: "a@example.com"}, ""},
		{"length word that does not match", hxbuild.AccountSpec{Address: "a@example.com", LengthBytes: 5}, ""},
		{"not an address", hxbuild.AccountSpec{Address: "Fixture text"}, ""},
		{"no local part", hxbuild.AccountSpec{Address: "@example.com"}, ""},
		{"no domain", hxbuild.AccountSpec{Address: "a@"}, ""},
		{"two at signs", hxbuild.AccountSpec{Address: "a@b@example.com"}, ""},
		{"a space", hxbuild.AccountSpec{Address: "a b@example.com"}, ""},
	}
	for _, c := range cases {
		got, ok := AccountAddress(accountObject(t, c.spec))
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v", c.name, got, ok)
		}
	}
}

func TestAccountAddressRefusesOtherLayouts(t *testing.T) {
	if _, ok := AccountAddress(accountObject(t, hxbuild.AccountSpec{Address: "a@example.com", Tag: 0x19d1})); ok {
		t.Fatal("another tag read")
	}
	var o hxstore.Object
	s := storeOf(t, framed(hxbuild.NewObject(0x55, 32, 32)))
	_, _ = s.Walk(t.Context(), hxstore.WalkOptions{}, func(x hxstore.Object) error { o = x.Clone(); return nil })
	if _, ok := AccountAddress(o); ok {
		t.Fatal("another class read")
	}
	// An area-one word larger than the object is a damaged record, not an address.
	b := accountObject(t, hxbuild.AccountSpec{Address: "a@example.com"})
	b.Raw[104], b.Raw[105], b.Raw[106], b.Raw[107] = 0xff, 0xff, 0xff, 0x7f
	if _, ok := AccountAddress(b); ok {
		t.Fatal("a lead past the object read")
	}
	// A string word that points outside the object.
	b = accountObject(t, hxbuild.AccountSpec{Address: "a@example.com"})
	b.Raw[5532], b.Raw[5533], b.Raw[5534], b.Raw[5535] = 0xff, 0xff, 0xff, 0x0f
	if _, ok := AccountAddress(b); ok {
		t.Fatal("a string word outside the object read")
	}
}

func TestAccountAddressNeedsTwoAlikeCopies(t *testing.T) {
	if _, ok := AccountAddress(accountObject(t, hxbuild.AccountSpec{Address: "a@example.com", SecondAddress: "b@example.com"})); ok {
		t.Fatal("two copies that differ were believed")
	}
	if a, ok := AccountAddress(accountObject(t, hxbuild.AccountSpec{Address: "a@example.com", SecondAddress: "A@Example.com"})); !ok || a != "a@example.com" {
		t.Fatalf("copies that differ in case only: %q %v", a, ok)
	}
	// The second copy's own length word is checked too.
	b := accountObject(t, hxbuild.AccountSpec{Address: "a@example.com"})
	b.Raw[5560], b.Raw[5561] = 3, 0
	if _, ok := AccountAddress(b); ok {
		t.Fatal("a second copy with a wrong length word was believed")
	}
}

func TestCollectAccountAddresses(t *testing.T) {
	events := []*hxbuild.Object{hxbuild.NewEvent(baseSpec(1))}
	acct := func(a string) *hxbuild.Object { return hxbuild.NewAccount(hxbuild.AccountSpec{Address: a}) }
	one := collect(t, storeOf(t, framed(append(events, acct("Fixture@example.com"), acct("fixture@EXAMPLE.com"))...)), Options{})
	if len(one.AccountAddresses) != 1 || one.AccountAddresses[0] != "fixture@example.com" || one.Notes.AccountAddresses != 1 || one.Notes.AccountObjects != 2 {
		t.Fatalf("%q %+v", one.AccountAddresses, one.Notes)
	}
	// Two accounts signed in to one profile give both, sorted.
	two := collect(t, storeOf(t, framed(append(events, acct("b@example.com"), acct("a@example.com"))...)), Options{})
	if len(two.AccountAddresses) != 2 || two.AccountAddresses[0] != "a@example.com" || two.AccountAddresses[1] != "b@example.com" || two.Notes.AccountAddresses != 2 {
		t.Fatalf("%q %+v", two.AccountAddresses, two.Notes)
	}
	none := collect(t, storeOf(t, framed(append(events, hxbuild.NewAccount(hxbuild.AccountSpec{Text: "Fixture text"}))...)), Options{})
	if len(none.AccountAddresses) != 0 || none.Notes.AccountAddresses != 0 || none.Notes.AccountObjects != 0 {
		t.Fatalf("%q %+v", none.AccountAddresses, none.Notes)
	}
}
