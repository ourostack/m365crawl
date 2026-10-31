package scoutsessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"regexp"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// ScrubVersion numbers the code that applies the Scout rules below. Raise it when that code
// changes; a change to the rule table itself is covered by Stamp.
const ScrubVersion = 1

const redacted = "[redacted]"

// scrubRule replaces each match of pattern with [redacted], keeping the first capture group when
// keep is set (a header or key name before the secret).
type scrubRule struct {
	pattern *regexp.Regexp
	keep    bool
}

// scoutRules are the secret shapes that agent tool output carries and the Teams rules do not
// cover. They run on decoded JSON strings, so an escape can neither hide a secret nor be cut in
// half. A redacted value starts with "[", which no rule takes as a value, so a second pass finds
// nothing. Order matters: a key=value rule runs before the generic assignment rule.
var scoutRules = []scrubRule{
	{pattern: regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{16,}`), keep: true},
	{pattern: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})`)},
	{pattern: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{pattern: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{pattern: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}`)},
	{pattern: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----|[\s\S]*$)`)},
	{pattern: regexp.MustCompile(`(?i)\b((?:AccountKey|SharedAccessKey|SharedAccessSignature|Password|Pwd)=)[^;"\s\\\[][^;"\s\\]*`), keep: true},
	{pattern: regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|id_token|client_secret|code|token)=)[^&"\s\\\[][^&"\s\\]*`), keep: true},
	{pattern: regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_?KEY)\s*[=:]\s*["']?)[^\s"'\\\[][^\s"'\\]{7,}`), keep: true},
	{pattern: regexp.MustCompile(`(?i)\b((?:Set-)?Cookie:\s*)[^"\\\s\[][^"\\\n]*`), keep: true},
}

// Stamp identifies everything that decides what the archive keeps of a session file: the parser
// version, the Scout scrub code and rules, and the Teams scrub rules. An archived read made under
// another stamp is read again, or scrubbed again from what the archive holds. The digest only
// detects a changed rule table, so a fast non-cryptographic hash is enough.
func Stamp() string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "parser=%d scrub=%d teams=%s\n", ParserVersion, ScrubVersion, teamsdesktop.RulesStamp())
	for _, rule := range scoutRules {
		_, _ = fmt.Fprintf(h, "%t %s\n", rule.keep, rule.pattern)
	}
	return fmt.Sprintf("%d.%d.%016x", ParserVersion, ScrubVersion, h.Sum64())
}

// Scrub applies the Teams scrub rules to the raw JSON, then the Scout rules to every decoded
// string value, and returns the result with the number of redactions. Bytes with nothing to
// redact come back unchanged; bytes that are not JSON get only the Teams rules.
func Scrub(raw []byte) ([]byte, int) {
	out, n := teamsdesktop.Scrub(raw)
	clean, m, ok := scrubStrings(out)
	if !ok {
		return out, n
	}
	return clean, n + m
}

// scrubText applies the Scout rules to one decoded string.
func scrubText(s string) (string, int) {
	n := 0
	for _, rule := range scoutRules {
		matches := len(rule.pattern.FindAllStringIndex(s, -1))
		if matches == 0 {
			continue
		}
		n += matches
		if rule.keep {
			s = rule.pattern.ReplaceAllString(s, "${1}"+redacted)
		} else {
			s = rule.pattern.ReplaceAllLiteralString(s, redacted)
		}
	}
	return s, n
}

type scrubFrame struct {
	object bool
	n      int
}

// scrubStrings rebuilds one JSON value token by token, scrubbing string values (object keys
// stay as written) and keeping numbers verbatim. It returns the input unchanged when nothing was
// redacted, and ok false when the input is not exactly one JSON value.
func scrubStrings(data []byte) (_ []byte, redactions int, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var b bytes.Buffer
	var stack []scrubFrame
	for tokens := 0; ; tokens++ {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) && tokens > 0 && len(stack) == 0 {
			break
		}
		if err != nil {
			return nil, 0, false
		}
		key := false
		if delim, isDelim := tok.(json.Delim); len(stack) > 0 && (!isDelim || (delim != '}' && delim != ']')) {
			top := &stack[len(stack)-1]
			switch {
			case top.object && top.n%2 == 1:
				b.WriteByte(':')
			case top.n > 0:
				b.WriteByte(',')
			}
			key = top.object && top.n%2 == 0
			top.n++
		}
		switch v := tok.(type) {
		case json.Delim:
			b.WriteString(v.String())
			if v == '{' || v == '[' {
				stack = append(stack, scrubFrame{object: v == '{'})
			} else {
				stack = stack[:len(stack)-1]
			}
		case string:
			if !key {
				var n int
				v, n = scrubText(v)
				redactions += n
			}
			writeJSONString(&b, v)
		case json.Number:
			b.WriteString(v.String())
		case bool:
			_, _ = fmt.Fprint(&b, v)
		default:
			b.WriteString("null")
		}
		if len(stack) == 0 && dec.More() {
			return nil, 0, false
		}
	}
	if redactions == 0 {
		return data, 0, true
	}
	return b.Bytes(), redactions, true
}

func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Truncate(b.Len() - 1)
}
