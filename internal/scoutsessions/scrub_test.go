package scoutsessions

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every secret below is synthetic: made-up strings in the shapes the rules look for.
func TestScrubTextRedactsEverySecretShape(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"bearer mid-text":   {"curl -H 'Authorization: Bearer abcdefghijklmnop0123' x", "curl -H 'Authorization: Bearer [redacted]' x"},
		"github classic":    {"token ghp_" + strings.Repeat("a", 36) + " end", "token [redacted] end"},
		"github fine":       {"github_pat_" + strings.Repeat("B_", 31), "[redacted]"},
		"openai style":      {"key sk-" + strings.Repeat("c", 24), "key [redacted]"},
		"slack":             {"xoxb-0123456789-abc", "[redacted]"},
		"aws":               {"id AKIA" + strings.Repeat("Z", 16) + " ok", "id [redacted] ok"},
		"npm":               {"npm_" + strings.Repeat("d", 36), "[redacted]"},
		"pem":               {"a\n-----BEGIN RSA PRIVATE KEY-----\nMIIsynthetic\n-----END RSA PRIVATE KEY-----\nb", "a\n[redacted]\nb"},
		"pem cut off":       {"x -----BEGIN PRIVATE KEY-----\nMIIsynthetic", "x [redacted]"},
		"connection string": {"Server=s;Password=hunter2synthetic;AccountKey=abc==;", "Server=s;Password=[redacted];AccountKey=[redacted];"},
		"query token":       {"https://h/cb?code=abc123&state=s&access_token=xyz", "https://h/cb?code=[redacted]&state=s&access_token=[redacted]"},
		"env assignment":    {"export GITHUB_TOKEN=synthetic0123 and API_KEY: 'syntheticvalue'", "export GITHUB_TOKEN=[redacted] and API_KEY: '[redacted]'"},
		"cookie":            {"Set-Cookie: session=abc; Path=/\nnext", "Set-Cookie: [redacted]\nnext"},
		"nothing":           {"plain words about a token budget", "plain words about a token budget"},
	} {
		got, n := scrubText(tc.in)
		if got != tc.want || (n == 0) != (tc.in == tc.want) {
			t.Errorf("%s: %q (%d), want %q", name, got, n, tc.want)
		}
		// Already-redacted text is stable: a second pass finds nothing.
		if again, m := scrubText(got); again != got || m != 0 {
			t.Errorf("%s: second pass changed %q to %q (%d)", name, got, again, m)
		}
	}
}

func TestScrubKeepsJSONValidOnEscapedStrings(t *testing.T) {
	// The secrets sit inside JSON escapes (\n, \", \/, \u00XX), which a byte-level pass would cut.
	raw := `{"id":"s","name":"x","messages":[{"role":"tool","content":"line\n\"Authorization: Bearer abc\/defghijklmnopq\"\nPassword=p\u0040ss;\n-----BEGIN PRIVATE KEY-----\nMII\/abc\n-----END PRIVATE KEY-----","n":1.50,"b":true,"z":null,"k":"\u003c\u003e"}],"ghp_` + strings.Repeat("e", 36) + `":"key stays"}`
	out, n := Scrub([]byte(raw))
	if !json.Valid(out) || n != 3 {
		t.Fatalf("%d %s", n, out)
	}
	session, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	text := session.Messages[0].Text
	if strings.Contains(text, "defghij") || strings.Contains(text, "p@ss") || strings.Contains(text, "MII") || !strings.Contains(text, "Bearer [redacted]") {
		t.Fatalf("text %q", text)
	}
	for _, kept := range []string{`"n":1.50`, `"k":"<>"`, `"z":null`, `"b":true`, `"ghp_`} {
		if !strings.Contains(string(out), kept) {
			t.Fatalf("lost %s in %s", kept, out)
		}
	}
	// Nothing to redact: the bytes come back as they were.
	clean := `{"id":"s",  "messages":[{"content":"a\/b"}]}`
	if out, n := Scrub([]byte(clean)); string(out) != clean || n != 0 {
		t.Fatalf("%d %s", n, out)
	}
	// Not JSON: only the Teams rules apply, and the bytes stay unparsable.
	if out, _ := Scrub([]byte(`{"a":`)); string(out) != `{"a":` {
		t.Fatalf("%s", out)
	}
	for _, bad := range []string{`{"a":1}}`, `{"a":1} {"b":2}`, `{"a":[1`, ``} {
		if _, _, ok := scrubStrings([]byte(bad)); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestScrubAppliesTeamsRules(t *testing.T) {
	out, n := Scrub([]byte(`{"messages":[{"content":"Bearer x"}],"access_token":"synthetic"}`))
	if n != 2 || strings.Contains(string(out), "synthetic") || !json.Valid(out) {
		t.Fatalf("%d %s", n, out)
	}
}

func TestStampCoversParserAndBothRuleSets(t *testing.T) {
	stamp := Stamp()
	if !strings.HasPrefix(stamp, "1.1.") || stamp != Stamp() {
		t.Fatalf("%q", stamp)
	}
	saved := scoutRules
	t.Cleanup(func() { scoutRules = saved })
	scoutRules = append(append([]scrubRule{}, saved...), scrubRule{pattern: saved[0].pattern})
	if Stamp() == stamp {
		t.Fatal("a new rule did not change the stamp")
	}
}

// A secret can sit under a JSON key that names it, with nothing in the value that looks secret.
func TestScrubRedactsValuesUnderSecretKeys(t *testing.T) {
	raw := `{"messages":[{"role":"tool","toolCalls":[{"args":{"api_key":"opaque1","token":"o2","env":{"GITHUB_TOKEN":"o3","x-api-key":"o4","clientSecret":"o7","client_secret":"o5","Access-Key":"o6"},` +
		`"max_tokens":"5","tokens":"7","token":"[redacted]","password":12345,"secret":{"value":"nested"}}}],"content":"plain"}]}`
	out, n := Scrub([]byte(raw))
	// Seven by key name, and the Teams rules take the numeric password whole.
	if !json.Valid(out) || n != 8 {
		t.Fatalf("%d %s", n, out)
	}
	for _, gone := range []string{"opaque1", `"o2"`, "o3", "o4", "o5", "o6", "o7", "12345"} {
		if strings.Contains(string(out), gone) {
			t.Fatalf("%s kept in %s", gone, out)
		}
	}
	for _, kept := range []string{`"max_tokens":"5"`, `"tokens":"7"`, `"value":"nested"`, `"api_key":"[redacted]"`} {
		if !strings.Contains(string(out), kept) {
			t.Fatalf("%s lost in %s", kept, out)
		}
	}
	if again, m := Scrub(out); m != 0 || string(again) != string(out) {
		t.Fatalf("second pass: %d", m)
	}
}

func TestScrubTextRedactsQuotedAndHyphenatedNames(t *testing.T) {
	for in, want := range map[string]string{ // #nosec G101 -- synthetic values in secret shapes
		`{"GITHUB_TOKEN": "abcdefgh12"}`: `{"GITHUB_TOKEN": "[redacted]"}`,
		`{'api_key': 'abcdefgh123'}`:     `{'api_key': '[redacted]'}`,
		"x-api-key: abcdef123456":        "x-api-key: [redacted]",
		"MY-SECRET=abcdefgh12 next":      "MY-SECRET=[redacted] next",
		"the token: is short":            "the token: is short",
	} {
		if got, _ := scrubText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
