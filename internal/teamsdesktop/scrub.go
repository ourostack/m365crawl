package teamsdesktop

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// The rules Scrub applies (key names, regular expressions, depth limit) are the fields of rules, in
// rules.go; nothing in this file keeps a rule of its own, because MemoSignature reads rules and
// nothing else.

// Scrub removes credential material from a generic record's canonical JSON (a value or a key) and
// returns the scrubbed JSON with the number of redactions, each replaced by "[redacted]": the
// whole value (a string, number, object, array or null) of any object key named access_token,
// refresh_token, id_token, authorization, client_secret or password (ignoring case), the same
// for a V8 Map pair whose key is one of those names, the "value" field of an object whose
// "type", "name" or "key" field is one of those names, any string that starts with "Bearer"
// and whitespace (ignoring case), JWT-shaped substrings, and the value of a sig= query parameter
// (ignoring case). A string that holds JSON (an object or array, up to four levels deep) is
// scrubbed inside and written back as a string; one that is truncated or nested deeper has the
// whole string redacted if it names a credential key. A value with nothing to scrub comes back as is,
// and the output stays valid JSON.
func Scrub(valueJSON []byte) ([]byte, int) {
	if !MayRedact(valueJSON) {
		return valueJSON, 0
	}
	return scrub(valueJSON, 0)
}

func scrub(valueJSON []byte, depth int) ([]byte, int) {
	out, n := redactKeyed(valueJSON, depth)
	// A regular expression copies its whole input even when it matches nothing, so each runs only
	// when the text can hold what it looks for.
	if mayContain(out, []string{"bearer"}) {
		out = rules.Bearer.ReplaceAllFunc(out, func([]byte) []byte { n++; return []byte(`"` + redacted + `"`) })
	}
	if mayContain(out, []string{"eyJ"}) {
		out = rules.JWT.ReplaceAllFunc(out, func([]byte) []byte { n++; return []byte(redacted) })
	}
	if mayContain(out, []string{"sig="}) {
		out = rules.Sig.ReplaceAllFunc(out, func(m []byte) []byte {
			n++
			i := rules.Sig.FindSubmatchIndex(m)[3]
			return append(append([]byte(nil), m[:i]...), redacted...)
		})
	}
	return out, n
}

// redactKeyed walks the JSON text once, tracking string literals. It replaces the value that
// follows a credential-named string (after "name": in an object, or after "name", when the name
// is the first element of an array, as in a V8 Map pair [key,value]), the "value" field of an
// object named by a sibling field, and scrubs strings that hold JSON.
func redactKeyed(in []byte, depth int) ([]byte, int) {
	var out bytes.Buffer
	out.Grow(len(in) + len(in)/8)
	n := 0
	var siblings map[int]int // start of a "value" field's value -> its end; made when a span is found
	for i := 0; i < len(in); {
		if end, ok := siblings[i]; ok {
			out.WriteString(`"` + redacted + `"`)
			n++
			i = end
			continue
		}
		if in[i] == '{' {
			for start, end := range siblingValues(in[i:valueEnd(in, i)]) {
				if siblings == nil {
					siblings = map[int]int{}
				}
				siblings[i+start] = i + end
			}
		}
		if in[i] != '"' {
			out.WriteByte(in[i])
			i++
			continue
		}
		start := i
		i = stringEnd(in, start)
		lit := in[start:i]
		if !secretKeyLiteral(lit) {
			if inner, k := scrubStringified(lit, depth); k > 0 {
				lit = inner
				n += k
			}
			out.Write(lit)
			continue
		}
		out.Write(lit)
		j := skipSpace(in, i)
		if j >= len(in) {
			continue
		}
		if in[j] == ':' || (in[j] == ',' && bytes.HasSuffix(bytes.TrimRight(in[:start], " \t\r\n"), []byte("["))) {
			k := skipSpace(in, j+1)
			out.Write(in[i:k])
			i = valueEnd(in, k)
			out.WriteString(`"` + redacted + `"`)
			n++
		}
	}
	return out.Bytes(), n
}

// secretKeyLiteral reports whether a JSON string literal (quotes included) names a credential.
func secretKeyLiteral(lit []byte) bool {
	// A decoded character takes at least a sixth of the escape that spells it, so a literal far
	// longer than the longest credential name cannot decode to one. Decoding it would copy it.
	longest := 0
	for k := range rules.SecretKeys {
		longest = max(longest, len(k))
	}
	if len(lit) > 6*longest+2 {
		return false
	}
	var s string
	if json.Unmarshal(lit, &s) != nil {
		return false
	}
	return rules.SecretKeys[strings.ToLower(s)]
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
	for j < len(in) && !strings.ContainsRune(",}] \t\r\n", rune(in[j])) {
		j++
	}
	return j
}

// skipSpace returns the index of the first non-whitespace byte at or after i.
func skipSpace(in []byte, i int) int {
	for i < len(in) && strings.ContainsRune(" \t\r\n", rune(in[i])) {
		i++
	}
	return i
}

// siblingValues takes the text of one JSON object and, when its "type", "name" or "key" field is
// a credential name, returns the span (start and end offsets) of its "value" field's value.
func siblingValues(obj []byte) map[int]int {
	named := false
	spans := map[int]int{}
	i := skipSpace(obj, 1)
	for i < len(obj) && obj[i] == '"' {
		ke := stringEnd(obj, i)
		var key string
		_ = json.Unmarshal(obj[i:ke], &key)
		vs := skipSpace(obj, ke)
		vs = skipSpace(obj, vs+1)
		ve := valueEnd(obj, vs)
		switch lk := strings.ToLower(key); {
		case slices.Contains(rules.SiblingNameFields, lk):
			if secretKeyLiteral(obj[vs:ve]) {
				named = true
			}
		case lk == rules.SiblingValueField:
			spans[vs] = ve
		}
		i = skipSpace(obj, ve)
		i = skipSpace(obj, i+1)
	}
	if !named {
		return nil
	}
	return spans
}

// scrubStringified scrubs a string literal that holds a JSON object or array and returns the
// literal re-encoded with the redactions, or the literal and 0 when there is nothing to change.
func scrubStringified(lit []byte, depth int) ([]byte, int) {
	// The text must start, after any white space, with { or [. Decoding a long literal copies it, so
	// a literal whose first character is plainly something else (HTML starts with <) is left alone.
	if len(lit) > 1 && lit[1] < 0x80 && lit[1] != ' ' && lit[1] != '\\' && lit[1] != '{' && lit[1] != '[' {
		return lit, 0
	}
	var text string
	if json.Unmarshal(lit, &text) != nil {
		return lit, 0
	}
	t := strings.TrimSpace(text)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return lit, 0
	}
	if depth >= rules.MaxStringifiedDepth || !json.Valid([]byte(t)) {
		// Too deep to unwrap, or truncated JSON: redact the whole string when it names a credential.
		if rules.SecretName.MatchString(t) {
			return []byte(`"` + redacted + `"`), 1
		}
		return lit, 0
	}
	inner, n := scrub([]byte(t), depth+1)
	if n == 0 {
		return lit, 0
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(inner))
	return bytes.TrimRight(buf.Bytes(), "\n"), n
}
