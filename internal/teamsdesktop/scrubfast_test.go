package teamsdesktop

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// scrubCases mixes text that must be scrubbed with text that looks close to it.
var scrubCases = []string{
	``, `plain meeting text`, `<p>Join: https://example.invalid/join?x=1&y=2</p> call +1 555 0100`,
	`{"password":"hunter2"}`, `{"PassWord":"x"}`, `{"name":"password","value":"x"}`, `[ "password" , "x" ]`,
	`"Bearer abc.def"`, `"bearer\tabc"`, `see https://x.invalid/a?sig=ABC123&b=1`, `https://x.invalid/a?SIG=ABC`,
	`token eyJhbGciOi.eyJzdWIiOi.abc_-`, `eyJ only`, `sig= alone`, `{"a":"{\"password\":\"x\"}"}`,
	`{"a":"{\"password\":\"x\"}"}`, `{"password":"x"}`, `{"PASSWORD":"x"}`, `<a href="?sig=ABC">`,
	`{"paſsword":"x"}`, `?ſig=ABC`, "Key", `{"access_token":"a","refresh_token":"b","id_token":"c"}`,
	`a <b> & & c`, `\u00zz \u00`, `{"client_secret":"s"}`, `{"authorization":"Basic x"}`,
	`{"a":"[1,2,{\"id_token\":\"x\"}]"}`, strings.Repeat("lorem ipsum dolor ", 50) + `password`,
}

// MayRedact only says false when Scrub would change nothing, and the answer comes back unchanged.
func checkFast(t *testing.T, in []byte) {
	t.Helper()
	want, wn := scrub(in, 0) // the full scrub, with no pre-check
	got, gn := Scrub(in)
	if !bytes.Equal(got, want) || gn != wn {
		t.Fatalf("Scrub(%q) = %q, %d; full scrub gives %q, %d", in, got, gn, want, wn)
	}
	if !MayRedact(in) && (wn != 0 || !bytes.Equal(want, in)) {
		t.Fatalf("MayRedact(%q) is false, but the full scrub gives %q, %d", in, want, wn)
	}
	// The plain-text form, as the Outlook reader passes it, answers the same way for the string
	// it encodes to.
	var s string
	if json.Unmarshal(in, &s) == nil {
		enc, _ := json.Marshal(s)
		out, n := scrub(enc, 0)
		if !MayRedact(s) && (n != 0 || !bytes.Equal(out, enc)) {
			t.Fatalf("MayRedact(%q) is false, but scrubbing its encoding gives %q, %d", s, out, n)
		}
	}
}

func init() {
	bs := string(rune(92))
	scrubCases = append(scrubCases,
		`{"a":"{`+bs+`"pass`+bs+`u0077ord`+bs+`":`+bs+`"x`+bs+`"}"}`, `{"`+bs+`u0070assword":"x"}`, `{"`+bs+`u0050ASSWORD":"x"}`,
		`<a href="?`+bs+`u0073ig=ABC">`, `{"pass`+bs+`u212aword":"x"}`, `{"pas`+bs+`u017fword":"x"}`, `a `+bs+`u003cb`+bs+`u003e `+bs+`u0026 c`)
}

func TestScrubFastPathMatchesFullScrub(t *testing.T) {
	for _, c := range scrubCases {
		checkFast(t, []byte(c))
		enc, _ := json.Marshal(c)
		checkFast(t, enc)
		checkFast(t, []byte(strings.ToUpper(string(enc))))
	}
	var plain, spelled int
	for _, c := range scrubCases {
		if MayRedact(c) {
			spelled++
		} else {
			plain++
		}
	}
	if plain < 3 || spelled < 15 {
		t.Fatalf("the cases test one side only: %d plain, %d flagged", plain, spelled)
	}
	if got, n := Scrub([]byte(`"nothing here"`)); n != 0 || string(got) != `"nothing here"` {
		t.Fatalf("%q %d", got, n)
	}
}

func FuzzScrubFastPath(f *testing.F) {
	for _, c := range scrubCases {
		f.Add([]byte(c))
		enc, _ := json.Marshal(c)
		f.Add(enc)
	}
	f.Fuzz(func(t *testing.T, in []byte) { checkFast(t, in) })
}

// indexFold finds what the lower-casing version found, at the same byte offsets.
func TestIndexFoldMatchesLowerCasedIndex(t *testing.T) {
	for _, c := range [][2]string{{"", ""}, {"abc", ""}, {"a</SCRIPT>b", "</script>"}, {"a</scrip", "</script>"}, {"x", "xy"}, {"ĀBC</Style>", "</style>"}, {"nothing", "</style>"}, {"</STYLE></style>", "</style>"}} {
		if got, want := indexFold(c[0], c[1]), strings.Index(asciiLower(c[0]), asciiLower(c[1])); got != want {
			t.Errorf("indexFold(%q, %q) = %d, want %d", c[0], c[1], got, want)
		}
	}
}

// The scan's edge cases: bytes that begin a folding character but are not one, escapes that are
// cut short, not hex, or not a name character.
func TestMayRedactEdges(t *testing.T) {
	bs := string(rune(92))
	esc := func(hex string) string { return bs + "u" + hex }
	for in, want := range map[string]bool{
		"\u014c":                        false, // starts with 0xc5, is not the long s
		"\u2603":                        false,
		"ends \xc5":                     false,
		"ends \xe2\x84":                 false,
		"ends " + bs + "u00":            false,
		bs + "n " + bs + "t " + bs + bs: false,
		esc("003c") + esc("003e"):       false,
		esc("00e9"):                     false, // above ASCII, not a folding character
		esc("0070"):                     true,  // p
		esc("0039"):                     true,  // 9
		esc("005f"):                     true,  // _
		esc("212A"):                     true,  // the Kelvin sign
		esc("017F"):                     true,  // the long s
		esc("00gg"):                     false,
		"the PASSWORD":                  true,
		"eyJ":                           true,
		"EYJ":                           false, // the token prefix is case-sensitive
	} {
		if got := MayRedact(in); got != want {
			t.Errorf("MayRedact(%q) = %v, want %v", in, got, want)
		}
	}
}
