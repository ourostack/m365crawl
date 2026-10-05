package teamsdesktop

import (
	"strings"
	"unicode"
)

// deniedTerms are the substrings that mark a database or object store name as credential
// material. Matching is case-insensitive. Over-denying is acceptable: every denied name is
// reviewed on the work machine, and a wrongly denied one is cheaper than a decoded token.
var deniedTerms = []string{
	"auth", "token", "credential", "secret", "cookie", "msal", "oneauth",
	"key-store", "keystore", "keyval", "session",
	"key-value", "keyring", "crypto", "encrypt", "pkce", "bearer", "password", "refresh", "adal",
}

// deniedTokens are terms too short to match as substrings ("aad" is in "load"): they match only
// as a whole segment, delimited by the start or end of the name, one of - _ : . / or a camelCase
// boundary (userKeys has the segments user and keys; monkeys has one).
var deniedTokens = []string{"aad", "keys", "jwt", "e2ee"}

// Denied reports whether a database name or an object store name looks like credential material:
// it starts with "Teams:auth", contains one of the denied terms or has one of the denied tokens as a whole segment, ignoring case. Denied names are
// never listed beyond their count, walked or decoded.
func Denied(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "teams:auth") {
		return true
	}
	for _, t := range deniedTerms {
		if strings.Contains(n, t) {
			return true
		}
	}
	for _, seg := range segments(name) {
		for _, t := range deniedTokens {
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
