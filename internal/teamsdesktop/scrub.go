package teamsdesktop

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// redacted replaces the secret part of a generic record's value.
const redacted = "[redacted]"

var (
	// jwtRE matches a JSON Web Token: a header and a payload (both base64url JSON, so both start
	// with "eyJ") and a signature.
	jwtRE = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	// bearerRE matches a whole JSON string that starts with "Bearer " (an opaque bearer token).
	bearerRE = regexp.MustCompile(`(?i)"bearer (?:[^"\\]|\\.)*"`)
	// sigRE matches the value of a sig= query parameter (a signed URL's signature), any case.
	sigRE = regexp.MustCompile(`(?i)([?&]sig=)[^&"\\\s#]+`)
)

// secretKeys are the object keys (compared in lower case) whose value is always credential material.
var secretKeys = map[string]bool{
	"access_token": true, "refresh_token": true, "id_token": true,
	"authorization": true, "client_secret": true, "password": true,
}

// Scrub removes credential material from a generic record's canonical JSON (a value or a key) and
// returns the scrubbed JSON with the number of redactions, each replaced by "[redacted]": the
// whole value (a string, number, object, array or null) of any object key named access_token,
// refresh_token, id_token, authorization, client_secret or password (ignoring case), the same
// for a V8 Map pair whose key is one of those names, any string that starts with "Bearer "
// (ignoring case), JWT-shaped substrings, and the value of a sig= query parameter (ignoring
// case). A value with nothing to scrub comes back as is, and the output stays valid JSON.
func Scrub(valueJSON []byte) ([]byte, int) {
	out, n := redactKeyed(valueJSON)
	out = bearerRE.ReplaceAllFunc(out, func([]byte) []byte { n++; return []byte(`"` + redacted + `"`) })
	out = jwtRE.ReplaceAllFunc(out, func([]byte) []byte { n++; return []byte(redacted) })
	out = sigRE.ReplaceAllFunc(out, func(m []byte) []byte {
		n++
		i := sigRE.FindSubmatchIndex(m)[3]
		return append(append([]byte(nil), m[:i]...), redacted...)
	})
	return out, n
}

// redactKeyed walks the JSON text once, tracking string literals, and replaces the value that
// follows a credential-named string: after "name": in an object, or after "name", when the name
// is the first element of an array (a V8 Map pair is [key,value]).
func redactKeyed(in []byte) ([]byte, int) {
	var out bytes.Buffer
	n := 0
	for i := 0; i < len(in); {
		if in[i] != '"' {
			out.WriteByte(in[i])
			i++
			continue
		}
		end := stringEnd(in, i)
		lit := in[i:end]
		out.Write(lit)
		i = end
		if !secretKeyLiteral(lit) || i >= len(in) {
			continue
		}
		if in[i] == ':' || (in[i] == ',' && bytes.HasSuffix(out.Bytes()[:out.Len()-len(lit)], []byte("["))) {
			out.WriteByte(in[i])
			i++
			i = valueEnd(in, i)
			out.WriteString(`"` + redacted + `"`)
			n++
		}
	}
	return out.Bytes(), n
}

// secretKeyLiteral reports whether a JSON string literal (quotes included) names a credential.
func secretKeyLiteral(lit []byte) bool {
	var s string
	if json.Unmarshal(lit, &s) != nil {
		return false
	}
	return secretKeys[strings.ToLower(s)]
}

// stringEnd returns the index just past the string literal that starts at in[i] (a quote).
func stringEnd(in []byte, i int) int {
	for j := i + 1; j < len(in); j++ {
		switch in[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(in)
}

// valueEnd returns the index just past the JSON value that starts at in[i].
func valueEnd(in []byte, i int) int {
	if i >= len(in) {
		return i
	}
	switch in[i] {
	case '"':
		return stringEnd(in, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(in); {
			switch in[j] {
			case '"':
				j = stringEnd(in, j)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
			j++
		}
		return len(in)
	}
	j := i
	for j < len(in) && in[j] != ',' && in[j] != '}' && in[j] != ']' {
		j++
	}
	return j
}
