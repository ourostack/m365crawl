package teamsdesktop

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// MayRedact reports whether Scrub could change s, which is JSON text or the plain text a JSON string
// is made from. A false answer is a promise: Scrub would remove nothing and return the same bytes.
// It is a cheap scan, so a caller with a long body of ordinary text can skip the scrub and the
// encoding around it. Every rule needs a credential name, "bearer", "eyJ" or "sig=" in the text,
// or an escape or a non-ASCII character that spells one of their letters; any of them answers true.
// The matching is case-insensitive, and the characters that count are not listed by hand: they are
// every character that lower-cases to one of the letters, or folds with one (the Kelvin sign for
// k, the long s for s, the dotted capital I for i), found once from the Unicode tables.
func MayRedact[T ~string | ~[]byte](s T) bool { return mayContain(s, redactNeedles()) }

// foldTriggers are the non-ASCII characters that can stand for a letter of a rule's text.
type foldTriggers struct {
	runes map[rune]bool
	lead  [256]bool // the first byte of each, in UTF-8
	wide  bool      // one of them is outside the Basic Multilingual Plane
}

var (
	triggersOnce sync.Once
	triggersSet  foldTriggers
)

// nonASCIITriggers finds, over every character, those whose lower case or case-folding orbit holds
// a letter (or the underscore or equals sign) of a rule's text. It runs once.
func nonASCIITriggers() *foldTriggers {
	triggersOnce.Do(func() {
		alphabet := map[rune]bool{}
		for _, n := range redactNeedles() {
			for _, c := range strings.ToLower(n) {
				alphabet[c] = true
			}
		}
		spelled := func(r rune) bool { return r < utf8.RuneSelf && alphabet[unicode.ToLower(r)] }
		triggersSet.runes = map[rune]bool{}
		for r := rune(utf8.RuneSelf); r <= unicode.MaxRune; r++ {
			hit := spelled(unicode.ToLower(r))
			for f := unicode.SimpleFold(r); f != r && !hit; f = unicode.SimpleFold(f) {
				hit = spelled(f)
			}
			if hit {
				triggersSet.runes[r] = true
				var buf [4]byte
				n := utf8.EncodeRune(buf[:], r)
				triggersSet.lead[buf[0]] = true
				triggersSet.wide = triggersSet.wide || n == 4
			}
		}
	})
	return &triggersSet
}

// mayContain reports whether s holds one of needles (see MayRedact for how it reads s) or could,
// through an escape or a folding character, spell one.
func mayContain[T ~string | ~[]byte](s T, needles []string) bool {
	var first [256]bool
	for _, n := range needles {
		first[n[0]] = true
		if n[0] >= 'a' && n[0] <= 'z' {
			first[n[0]-'a'+'A'] = true
		}
	}
	tr := nonASCIITriggers()
	for b, on := range tr.lead {
		first[b] = first[b] || on
	}
	first['\\'] = true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !first[c] {
			continue
		}
		switch {
		case c == '\\':
			if escapeSpellsName(s, i, tr) {
				return true
			}
			continue
		case c >= utf8.RuneSelf:
			if r, _ := utf8.DecodeRuneInString(string(s[i:min(i+utf8.UTFMax, len(s))])); tr.runes[r] {
				return true
			}
			continue
		}
		for _, n := range needles {
			if foldHas(s[i:], n) {
				return true
			}
		}
	}
	return false
}

// redactNeedles are the lower-case texts one of which any redaction needs. "eyJ" is kept in its
// own case by foldHas.
func redactNeedles() []string {
	out := []string{"eyJ", "bearer", "sig="}
	for k := range rules.SecretKeys {
		out = append(out, k)
	}
	return out
}

// foldHas reports whether s starts with needle, ignoring ASCII case unless needle has upper case.
func foldHas[T ~string | ~[]byte](s T, needle string) bool {
	if len(s) < len(needle) {
		return false
	}
	exact := needle != strings.ToLower(needle)
	for j := 0; j < len(needle); j++ {
		c := s[j]
		if !exact && c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != needle[j] {
			return false
		}
	}
	return true
}

// escapeSpellsName reports whether the backslash at s[i] starts a \u escape of a character that
// could be part of a needle: a letter, digit, underscore, hyphen or equals sign, or a character in
// tr. A surrogate escape counts when any such character lies beyond the Basic Multilingual Plane.
// The escapes encoding/json writes for text (<, > and & as \u003c, \u003e, \u0026) are not such
// characters.
func escapeSpellsName[T ~string | ~[]byte](s T, i int, tr *foldTriggers) bool {
	if i+5 >= len(s) || s[i+1] != 'u' {
		return false
	}
	v := 0
	for _, h := range []byte{s[i+2], s[i+3], s[i+4], s[i+5]} {
		switch {
		case h >= '0' && h <= '9':
			v = v<<4 | int(h-'0')
		case h >= 'a' && h <= 'f':
			v = v<<4 | int(h-'a'+10)
		case h >= 'A' && h <= 'F':
			v = v<<4 | int(h-'A'+10)
		default:
			return false // not an escape: the text is not changed by it
		}
	}
	switch {
	case v >= 0xd800 && v <= 0xdfff:
		return tr.wide
	case v >= utf8.RuneSelf:
		return tr.runes[rune(v)]
	}
	c := byte(v)
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-' || c == '='
}
