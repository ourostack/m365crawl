package teamsdesktop

import "strings"

// MayRedact reports whether Scrub could change s, which is JSON text or the plain text a JSON string
// is made from. A false answer is a promise: Scrub would remove nothing and return the same bytes.
// It is a cheap scan, so a caller with a long body of ordinary text can skip the scrub and the
// encoding around it. Every rule needs a credential name, "bearer", "eyJ" or "sig=" in the text
// (the matching is case-insensitive, and Unicode folding also lets the Kelvin sign stand for k and
// the long s for s), or an escape that spells one of those letters; any of them answers true.
func MayRedact[T ~string | ~[]byte](s T) bool { return mayContain(s, redactNeedles()) }

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
	first['\\'], first[0xc5], first[0xe2] = true, true, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !first[c] {
			continue
		}
		switch c {
		case '\\':
			if escapeSpellsName(s, i) {
				return true
			}
			continue
		case 0xc5:
			if i+1 < len(s) && s[i+1] == 0xbf { // the long s
				return true
			}
			continue
		case 0xe2:
			if i+2 < len(s) && s[i+1] == 0x84 && s[i+2] == 0xaa { // the Kelvin sign
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
// could be part of a needle (a letter, digit, underscore, hyphen or equals sign) or the long s or
// Kelvin sign. The escapes encoding/json writes for text (<, > and & as <, >, &)
// are not such characters.
func escapeSpellsName[T ~string | ~[]byte](s T, i int) bool {
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
	case v == 0x17f, v == 0x212a:
		return true
	case v >= 0x80:
		return false
	}
	c := byte(v)
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-' || c == '='
}
