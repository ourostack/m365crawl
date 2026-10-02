package teamsdesktop

import "strings"

// deniedTerms are the substrings that mark a database or object store name as credential
// material. Matching is case-insensitive. Over-denying is acceptable: every denied name is
// reviewed on the work machine, and a wrongly denied one is cheaper than a decoded token.
var deniedTerms = []string{
	"auth", "token", "credential", "secret", "cookie", "msal", "oneauth",
	"key-store", "keystore", "keyval", "session",
	"key-value", "keyring", "crypto", "encrypt", "pkce", "bearer", "password", "refresh", "adal",
}

// deniedTokens are terms too short to match as substrings ("aad" is in "load"): they match only
// as a whole segment, delimited by the start or end of the name or one of - _ : .
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
	for _, seg := range strings.FieldsFunc(n, func(r rune) bool { return r == '-' || r == '_' || r == ':' || r == '.' }) {
		for _, t := range deniedTokens {
			if seg == t {
				return true
			}
		}
	}
	return false
}
