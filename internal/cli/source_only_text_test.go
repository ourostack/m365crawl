package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSourceOnlyTextPreservesNoticesWithoutLegacyFreshness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceOnly bool
		items      []any
		notices    []string
	}{
		{"empty", true, nil, []string{"subjects excluded because ownership is unresolved"}},
		{"nonempty", true, []any{peopleObservationItem{ID: "person-observation:1"}}, []string{"source claims are unresolved"}},
		{"projected", true, []any{map[string]any{"id": "person-observation:1"}}, []string{"first source notice", "second source notice"}},
		{"no-notices", true, nil, nil},
		{"ordinary", false, nil, []string{"ordinary notice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, color := range []bool{false, true} {
				var out bytes.Buffer
				rt := &runtime{stdout: &out, color: color}
				result := newList(tc.items, false)
				result.sourceOnly = tc.sourceOnly
				result.Notices = tc.notices
				result.Note = "fixture empty explanation"
				if err := rt.renderText("people", result); err != nil {
					t.Fatal(err)
				}
				text := out.String()
				for _, notice := range tc.notices {
					if strings.Count(text, "notice: "+notice) != 1 {
						t.Fatalf("notice must appear once: color=%v notice=%q output=%q", color, notice, text)
					}
				}
				if strings.Count(text, "notice: ") != len(tc.notices) {
					t.Fatalf("invented or missing notice: %q", text)
				}
				if !strings.Contains(text, result.Note) {
					t.Fatalf("empty explanation lost: %q", text)
				}
				if strings.Contains(text, "archive age:") == tc.sourceOnly {
					t.Fatalf("legacy freshness scope changed: %q", text)
				}
			}
		})
	}
}

func TestPeopleAccountExcludedTextPreservesJSONMeaning(t *testing.T) {
	for _, projected := range []bool{false, true} {
		e := newEnv(t)
		peopleFixture(t, e)
		args := []string{"--no-sync", "--account", "tenant/user", "people"}
		if projected {
			args = append(args, "--fields", "id")
		}
		code, jsonOut, stderr := e.run(args...)
		if code != 0 {
			t.Fatalf("JSON account exclusion: %d %s", code, stderr)
		}
		result := decode(t, jsonOut)
		notices, ok := result["notices"].([]any)
		if !ok || len(notices) == 0 || result["count"] != float64(0) || result["source_coverage"] == nil {
			t.Fatalf("JSON lost account exclusion: %s", jsonOut)
		}
		code, text, stderr := e.run(append([]string{"--format", "text"}, args...)...)
		if code != 0 {
			t.Fatalf("text account exclusion: %d %s", code, stderr)
		}
		for _, notice := range notices {
			value, ok := notice.(string)
			if !ok || !strings.Contains(text, value) {
				t.Fatalf("text lost JSON exclusion meaning: %s", text)
			}
		}
		if strings.Contains(text, "archive age: never synced") {
			t.Fatalf("source-only read gained legacy freshness: %s", text)
		}
	}
}
