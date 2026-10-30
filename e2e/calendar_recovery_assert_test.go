//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"
)

func assertScopedSyncHint(t *testing.T, e *env, hint any) {
	t.Helper()
	text, ok := hint.(string)
	if !ok || !strings.HasPrefix(text, "run ") {
		t.Fatalf("missing executable sync hint: %v", hint)
	}
	got := calendarRecoveryWords(t, strings.TrimPrefix(text, "run "))
	want := []string{"m365crawl", "--db", e.db, "--format", "json",
		"--teams-root", e.root, "--outlook-root", "none", "sync"}
	if !slices.Equal(got, want) {
		t.Fatalf("sync hint changed literal archive/root/format/operation context:\ngot  %q\nwant %q", got, want)
	}
}

func assertCalendarLinkFixes(t *testing.T, fixes []any, archive, teams, outlook string) {
	t.Helper()
	if len(fixes) != 2 {
		t.Fatalf("unlinked_fix %v", fixes)
	}
	for i, profile := range []string{"Main", "Second"} {
		command, ok := fixes[i].(string)
		if !ok {
			t.Fatalf("unlinked fix is not a sync instruction: %v", fixes[i])
		}
		words := calendarRecoveryWords(t, command)
		if len(words) == 0 || words[0] != "m365crawl" {
			t.Fatalf("unlinked fix is not a crawl instruction: %q", words)
		}
		flags, syncs := map[string]string{}, 0
		for j := 1; j < len(words); j++ {
			if words[j] == "sync" {
				syncs++
				continue
			}
			if !strings.HasPrefix(words[j], "--") || j+1 >= len(words) {
				t.Fatalf("unexpected unlinked-fix argument: %q", words)
			}
			if _, exists := flags[words[j]]; exists {
				t.Fatalf("duplicate context flag in unlinked fix: %q", words)
			}
			flags[words[j]] = words[j+1]
			j++
		}
		want := map[string]string{
			"--db": archive, "--teams-root": teams, "--outlook-root": outlook,
			"--format": "json", "--outlook-profile": profile,
			"--outlook-account": "<tenantId>/<userId>",
		}
		if syncs != 1 || len(flags) != len(want) {
			t.Fatalf("unlinked fix changed its operation/context: %q", words)
		}
		for flag, value := range want {
			if flags[flag] != value {
				t.Fatalf("unlinked fix lost %s literal context: %q", flag, words)
			}
		}
	}
}

// Parse only literal quoting emitted by the CLI; never expand or execute the instruction.
func calendarRecoveryWords(t *testing.T, command string) []string {
	t.Helper()
	var words []string
	var word strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(command) && command[i+1] == '\'':
			word.WriteByte('\'')
			i++
		case c == ' ' || c == '\t':
			if word.Len() != 0 {
				words = append(words, word.String())
				word.Reset()
			}
		default:
			word.WriteByte(c)
		}
	}
	if quote != 0 {
		t.Fatalf("unterminated literal quoting: %q", command)
	}
	if word.Len() != 0 {
		words = append(words, word.String())
	}
	return words
}
