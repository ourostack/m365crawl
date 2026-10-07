package outlookcal

import (
	"strings"
	"unicode/utf16"

	"github.com/ourostack/teamscrawl/internal/hxstore"
)

// The account object (docs/outlook-store.md): the record of an account signed in to the profile.
// Its own address sits at +5532 as a string word (base T = tag + the word at +104) with a length
// word after it, and the same address is written again at +5556 with its own words. The record is
// read for that one field and nothing else of it is mapped.
const (
	classAccount = 0x49
	tagAccount   = 0x19d0
	acAreaOne    = 104
	acAddress    = 5532
	acAddress2   = 5556
)

// AccountLayout is the account object the reader takes the profile's own address from.
var AccountLayout = Layout{Class: classAccount, Tag: tagAccount, Name: "account"}

// AccountAddress reads the account's own address from an account object: lower case, and only when
// it is there, looks like an address and is written twice alike. Class 0x49 holds other records of
// the same tag that carry no address; for them the string word is 0 and the length word is 0 (an
// absent string), so the length word has to match the text before the text is believed. ok is false
// for anything else.
func AccountAddress(o hxstore.Object) (addr string, ok bool) {
	if o.Class != classAccount || o.Tag != tagAccount {
		return "", false
	}
	lead, _ := o.U32(acAreaOne)
	if int64(lead) > int64(len(o.Raw)) {
		return "", false
	}
	first, ok := accountString(o, acAddress, tagAccount+int(lead))
	if !ok {
		return "", false
	}
	second, ok := accountString(o, acAddress2, tagAccount+int(lead))
	if !ok || second != first {
		return "", false
	}
	return first, true
}

// accountString is the address string whose word is at wordOff: present (its length word equals its
// byte length with the terminator) and shaped like an address. It is lower case.
func accountString(o hxstore.Object, wordOff, base int) (string, bool) {
	s, ok := o.StringUnaligned(wordOff, base)
	if !ok {
		return "", false
	}
	n, ok := o.U32(wordOff + 4)
	if !ok || int(n&^(1<<31)) != 2*len(utf16.Encode([]rune(s)))+2 {
		return "", false
	}
	if at := strings.IndexByte(s, '@'); at < 1 || at == len(s)-1 || strings.ContainsAny(s, " \t\r\n") || strings.Count(s, "@") != 1 {
		return "", false
	}
	return strings.ToLower(s), true
}
