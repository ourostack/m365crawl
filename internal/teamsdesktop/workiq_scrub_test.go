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
