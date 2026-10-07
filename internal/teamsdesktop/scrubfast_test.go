package teamsdesktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"
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

func init() {
	bs := string(rune(92))
	scrubCases = append(scrubCases,
		`0{"nAme"`, `{"name"`, `{"name":`, `{"İd_token":"x"}`, `{"authorİzation":"x"}`, `{"clİent_secret":"x"}`, `{"name":"clİent_secret","value":"x"}`,
		`{"a":"{`+bs+`"İd_token`+bs+`":`+bs+`"x`+bs+`"}"}`, `{"`+bs+`u0130d_token":"x"}`, `{"authorizat`+bs+`u0130on":"x"}`, `"BEARER x"`, `?SİG=x`, `eyJ`)
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
	for _, r := range []rune{0x130, 0x212a, 0x17f} {
		f.Add([]byte(`{"` + string(r) + `d_token":"x"}`))
		f.Add([]byte(`{"pa` + string(r) + `sword":"x"}`))
	}
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

// For every non-ASCII character that lower-cases or folds to a letter of a rule's text, a name
// spelled with it is scrubbed the same with and without the shortcut, and MayRedact never says
// false for one the full scrub changes. The characters are found from the Unicode tables, not listed.
func TestMayRedactEveryFoldingCharacter(t *testing.T) {
	bs := string(rune(92))
	checked := 0
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		lower := unicode.ToLower(r)
		spelling := lower < 0x80
		for f := unicode.SimpleFold(r); f != r && !spelling; f = unicode.SimpleFold(f) {
			spelling = f < 0x80
		}
		if !spelling {
			continue
		}
		for key := range rules.SecretKeys {
			for _, c := range []rune{lower, unicode.ToUpper(lower), unicode.SimpleFold(r)} {
				if c >= 0x80 || !strings.ContainsRune(key, c) {
					continue
				}
				name := strings.Replace(key, string(c), string(r), 1)
				escaped := strings.Replace(key, string(c), fmt.Sprintf(bs+"u%04x", r), 1)
				if r > 0xffff {
					escaped = name
				}
				for _, in := range []string{`{"` + name + `":"x"}`, `{"` + escaped + `":"x"}`, `{"name":"` + name + `","value":"x"}`, `{"a":"{` + bs + `"` + name + bs + `":` + bs + `"x` + bs + `"}"}`} {
					checkFast(t, []byte(in))
					checked++
				}
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d inputs checked", checked)
	}
	// The three the review found by hand.
	for _, r := range []rune{0x130, 0x212a, 0x17f} {
		if !nonASCIITriggers().runes[r] {
			t.Errorf("U+%04X is not a trigger", r)
		}
	}
	if bad := MayRedact(`{"` + string(rune(0x130)) + `d_token":"x"}`); !bad {
		t.Error("the dotted capital I in a key name is not flagged")
	}
}
