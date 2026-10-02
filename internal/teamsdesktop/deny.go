package teamsdesktop

import "strings"

// deniedTerms are the substrings that mark a database or object store name as credential
// material. Matching is case-insensitive. Over-denying is acceptable: every denied name is
// reviewed on the work machine, and a wrongly denied one is cheaper than a decoded token.
var deniedTerms = []string{
	"auth", "token", "credential", "secret", "cookie", "msal", "oneauth",
	"key-store", "keystore", "keyval", "session",
}

// Denied reports whether a database name or an object store name looks like credential material:
// it starts with "Teams:auth" or contains one of the denied terms, ignoring case. Denied names are
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
	return false
}
