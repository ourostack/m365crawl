package teamsdesktop

import "regexp"

// redacted replaces the secret part of a generic record's value.
const redacted = "[redacted]"

var (
	// jwtRE matches a JSON Web Token: a header and a payload (both base64url JSON, so both start
	// with "eyJ") and a signature.
	jwtRE = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	// secretKeyRE matches, in canonical JSON, a string value under a credential-named object key.
	secretKeyRE = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|authorization|client_secret|password)":)"(?:[^"\\]|\\.)*"`)
	// sigRE matches the value of a sig= query parameter (a signed URL's signature).
	sigRE = regexp.MustCompile(`([?&]sig=)[^&"\\\s#]+`)
)

// Scrub removes credential material from a generic record's canonical JSON value and returns the
// scrubbed JSON with the number of redactions: JWT-shaped substrings, the string value of any
// object key named access_token, refresh_token, id_token, authorization, client_secret or password
// (ignoring case), and the value of a sig= query parameter, each replaced by "[redacted]". A value
// with nothing to scrub comes back as is.
func Scrub(valueJSON []byte) ([]byte, int) {
	n := 0
	out := secretKeyRE.ReplaceAllFunc(valueJSON, func(m []byte) []byte {
		n++
		i := secretKeyRE.FindSubmatchIndex(m)[3]
		return append(append([]byte(nil), m[:i]...), `"`+redacted+`"`...)
	})
	out = jwtRE.ReplaceAllFunc(out, func([]byte) []byte { n++; return []byte(redacted) })
	out = sigRE.ReplaceAllFunc(out, func(m []byte) []byte {
		n++
		i := sigRE.FindSubmatchIndex(m)[3]
		return append(append([]byte(nil), m[:i]...), redacted...)
	})
	return out, n
}
