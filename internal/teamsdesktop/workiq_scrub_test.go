package teamsdesktop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkIQScrubWholeEnvelope(t *testing.T) {
	for _, raw := range []string{
		`{"@microsoft.graph.downloadUrl":"https://files.invalid/no-signature","keep":"text"}`,
		`{"@MICROSOFT.GRAPH.DOWNLOADURL":{"nested":"private-download"},"keep":"text"}`,
		`{"url":"https://files.invalid/?tempauth=private-download&x=1","keep":"text"}`,
		`{"url":"https://files.invalid/?x=1\u0026TeMpAuTh=private-download","keep":"text"}`,
		`{"url":"https://files.invalid/?x=1&tempauth\u003dprivate-download","keep":"text"}`,
		`{"wrapped":"{\"@microsoft.graph.downloadUrl\":\"private-download\"}","keep":"text"}`,
		`{"value":"private-download","name":"@microsoft.graph.downloadUrl","keep":"text"}`,
		`{"pairs":[["@microsoft.graph.downloadUrl","private-download"]],"keep":"text"}`,
	} {
		got, n := Scrub([]byte(raw))
		if !json.Valid(got) || n == 0 || !strings.Contains(string(got), `"keep":"text"`) {
			t.Errorf("server JSON not safely scrubbed: %s (%d)", got, n)
		}
		if strings.Contains(string(got), "private-download") || strings.Contains(string(got), "no-signature") {
			t.Errorf("preauthenticated server value survived: %s", got)
		}
	}
}

// A signed-URL value counts as one redaction however deeply it is wrapped: as plain text, as a JSON
// value, or inside JSON stored as a string. Enclosing levels and a second scrub find it already
// redacted and count nothing (#291).
func TestQueryRedactionCountsEachValueOnce(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{`https://example.invalid/?sig=SYNTHETIC`, 1},
		{`{"url":"https://example.invalid/?sig=SYNTHETIC"}`, 1},
		{`{"data":"{\"url\":\"https://example.invalid/?sig=SYNTHETIC\"}"}`, 1},
		{`{"data":"{\"url\":\"https://example.invalid/?tempauth=SYNTHETIC\"}"}`, 1},
		{`{"a":"https://example.invalid/?sig=ONE","b":"{\"c\":\"https://example.invalid/?x=1&sig=TWO\"}"}`, 2},
	} {
		encoded, _ := json.Marshal(tc.text)
		clean, n := Scrub(encoded)
		if n != tc.want || strings.Contains(string(clean), "SYNTHETIC") || strings.Contains(string(clean), "ONE") {
			t.Errorf("%s: %d redactions, want %d: %s", tc.text, n, tc.want, clean)
		}
		if again, n := Scrub(clean); n != 0 || string(again) != string(clean) {
			t.Errorf("%s: scrubbing scrubbed text counted %d more", tc.text, n)
		}
	}
}
