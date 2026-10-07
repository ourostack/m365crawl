package hxbuild

const (
	// ClassAccount and TagAccount name the account object: the record of a signed-in account of the
	// profile. Its fixed region is AccountSize bytes, equal to the tag.
	ClassAccount = 0x49
	TagAccount   = 0x19d0
	AccountSize  = 6608

	acAreaOne  = 104  // u32: size of area one, so the string area starts at AccountSize + this
	acAddress  = 5532 // string word (base T), length word at +4: the account's own address
	acAddress2 = 5556 // string word (base T): a second copy of the same address
)

// AccountSpec describes one class 0x49 object. An empty Address is written as an absent string
// (offset word 0, length word 0), the way a record of the same class and tag that carries no
// address looks.
type AccountSpec struct {
	// Tag overrides the envelope tag; zero means TagAccount. The fixed size stays AccountSize.
	Tag uint16
	// Address is the account's address; it is written at +5532 and again at +5556.
	Address string
	// Lead is the size of area one (the word at +104).
	Lead int
	// Text is written first in the string area and referenced by no word, the way another string
	// of the record sits there: an absent address then reads as this text.
	Text string
	// LengthBytes, when not zero, replaces the length word of the address at +5536: a test of a
	// word that does not match the text.
	LengthBytes uint32
	// SecondAddress, when not empty, is written at +5556 instead of Address: a record whose two
	// copies differ.
	SecondAddress string
}

// NewAccount builds an account object.
func NewAccount(s AccountSpec) *Object {
	tag := s.Tag
	if tag == 0 {
		tag = TagAccount
	}
	o := NewObject(ClassAccount, tag, AccountSize)
	o.PutU32(acAreaOne, clampU32(s.Lead))
	o.Append(make([]byte, s.Lead))
	base := AccountSize + s.Lead
	if s.Text != "" {
		o.AppendString(s.Text)
	}
	if s.Address == "" {
		return o
	}
	for _, w := range []int{acAddress, acAddress2} {
		text := s.Address
		if w == acAddress2 && s.SecondAddress != "" {
			text = s.SecondAddress
		}
		at := o.AppendString(text)
		o.PutU32(w, clampU32(at-base))
		n := uint32(len(UTF16Z(text)))
		if s.LengthBytes != 0 {
			n = s.LengthBytes
		}
		o.PutU32(w+4, n)
	}
	return o
}
