package teamsdesktop

import (
	"strings"
	"unicode"
)

// Denied reports whether a database name or an object store name looks like credential material:
// it starts with "Teams:auth", contains one of the denied terms or has one of the denied tokens as a whole segment, ignoring case. Denied names are
// never listed beyond their count, walked or decoded.
func Denied(name string) bool {
	n := strings.ToLower(name)
	for _, p := range rules.DeniedPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, t := range rules.DeniedTerms {
		if strings.Contains(n, t) {
			return true
		}
	}
	for _, seg := range segments(name) {
		for _, t := range rules.DeniedTokens {
			if seg == t {
				return true
			}
		}
	}
	return false
}

// segments splits a name at - _ : . / and at camelCase boundaries (a lower case letter or digit
// followed by an upper case letter, and the end of an upper case run before a capitalized word:
// AADToken is AAD and Token), and lower-cases each segment.
func segments(name string) []string {
	rs := []rune(name)
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	for i, r := range rs {
		switch r {
		case '-', '_', ':', '.', '/':
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) ||
				(unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1])) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}
