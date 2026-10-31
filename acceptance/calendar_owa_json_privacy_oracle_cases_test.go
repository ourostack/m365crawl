//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func TestOWACalendarStructuredPrivacyLiterals(t *testing.T) {
	for index, tc := range []struct {
		source, expected string
		count            int
	}{
		{`{"password":"SYNTHETIC","ok":1}`, `{"password":"[redacted]","ok":1}`, 1},
		{`{"pass\u0077ord":null}`, `{"pass\u0077ord":"[redacted]"}`, 1},
		{`{"Access_Token":{"nested":[1,2]},"password":false}`, `{"Access_Token":"[redacted]","password":"[redacted]"}`, 2},
		{`{"password":"FIRST","password":"SECOND"}`, `{"password":"[redacted]","password":"[redacted]"}`, 2},
		{`{"Value":[1,2],"TYPE":"password"}`, `{"Value":"[redacted]","TYPE":"password"}`, 1},
		{`{"$map":[["Password",{"x":1}],["k",1]]}`, `{"$map":[["Password","[redacted]"],["k",1]]}`, 1},
		{`  [ {"password" : 1}, "normal<&>" ]  `, `[ {"password" : "[redacted]"}, "normal<&>" ]`, 1},
		{`{"data":"{\"refresh_token\":\"SYNTHETIC\"}"}`, `{"data":"{\"refresh_token\":\"[redacted]\"}"}`, 1},
		{`{"data":"{\"url\":\"https://example.invalid/?sig=SYNTHETIC\"}"}`, `{"data":"{\"url\":\"https://example.invalid/?sig=[redacted]\"}"}`, 1},
		{`{"a":"Bearer SYNTHETIC","b":"https://example.invalid/?x=1&sig=SYNTHETIC"}`, `{"a":"[redacted]","b":"https://example.invalid/?x=1&sig=[redacted]"}`, 2},
		{` {"ok": 1, "text":"normal"} `, ` {"ok": 1, "text":"normal"} `, 0},
		{` [] `, ` [] `, 0},
	} {
		clean, count, err := owaOracleStructuredPrivacy(t.Context(), tc.source, 0)
		if err != nil || clean != tc.expected || count != tc.count {
			t.Fatal("independent_structural_privacy_literal_mismatch")
		}
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", "Subject", tc.source)
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		if err != nil || actual.Subject != tc.expected || notes.Redacted != tc.count {
			t.Fatalf("structured_privacy_actual_differential_mismatch case=%d redactions=%d expected=%d", index, notes.Redacted, tc.count)
		}
	}
}

func TestOWACalendarStructuredPrivacyRefusals(t *testing.T) {
	for _, source := range []string{`{"password":"SYNTHETIC"`, `false`, string([]byte{0xff}), `{"Bearer SYNTHETIC":1}`} {
		if _, _, err := owaOracleStructuredPrivacy(t.Context(), source, 0); err == nil {
			t.Fatal("unproved_structural_privacy_accepted")
		}
	}
	value := `{"password":"SYNTHETIC"}`
	for range 6 {
		encoded, err := json.Marshal(map[string]string{"data": value})
		if err != nil {
			t.Fatal(err)
		}
		value = string(encoded)
	}
	if _, _, err := owaOracleStructuredPrivacy(t.Context(), value, 0); err == nil {
		t.Fatal("unbounded_stringified_privacy_accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := owaOracleStructuredPrivacy(ctx, `{}`, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_structural_privacy_accepted")
	}
	for _, exhausted := range []owaPrivacyWalker{
		{ctx: t.Context(), nodes: owaMaxQueryRows},
		{ctx: t.Context(), bytes: owaMaxPayloadBytes},
	} {
		if _, _, err := exhausted.value([]byte(`{}`), 1, 0); err == nil || err.Error() != "calendar_oracle_json_budget_refused" {
			t.Fatal("exhausted_structural_budget_accepted")
		}
	}
	walker := owaPrivacyWalker{ctx: t.Context()}
	if _, _, err := walker.value([]byte(`{}`), 1, 129); err == nil {
		t.Fatal("unbounded_structural_depth_accepted")
	}
}
