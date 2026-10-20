package cli

import (
	"strings"
	"testing"
)

func assertCurrentLoopRetryTable(t *testing.T, text string) {
	t.Helper()
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || (fields[0] != "1" && fields[0] != "2" && fields[0] != "3") {
			continue
		}
		id, retry := fields[0], strings.Join(fields[6:], " ")
		if seen[id] {
			t.Fatalf("duplicate current retry row %s: %q", id, line)
		}
		seen[id] = true
		if id == "2" {
			if fields[1] != "failed" || !strings.HasPrefix(retry, "m365crawl ") {
				t.Fatalf("failed source lost its current retry: %q", line)
			}
		} else if fields[1] != "complete" || retry != "-" {
			t.Fatalf("healthy/metadata-only source acquired a current retry: %q", line)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("current retry table did not expose all three source states:\n%s", text)
	}
}
