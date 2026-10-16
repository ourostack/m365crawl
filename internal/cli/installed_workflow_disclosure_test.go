package cli

import (
	"strings"
	"testing"
)

func requireDeferredWorkflowMeaning(t *testing.T, text string) {
	t.Helper()
	text = strings.Join(strings.Fields(text), " ")
	for _, sentence := range []string{
		"Private Copilot history and Planner/Microsoft To Do assigned-task ingestion are not included and remain uncommitted/deferred.",
		"Shared-chat bot/citation claims and recap action items are not substitutes for those workflows.",
	} {
		if !strings.Contains(text, sentence) {
			t.Errorf("missing explicit noncoverage/non-substitution meaning %q", sentence)
		}
	}
}

func TestInstalledWorkflowDisclosureActualRootHelp(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"--no-color", "--help"}, {"--json", "--help"},
		{"--format", "text", "--no-color", "--help"}, {"--format", "log", "--no-color", "--help"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 || stderr != "" {
			t.Fatal(args, code, stderr)
		}
		requireDeferredWorkflowMeaning(t, out)
	}
}

func TestInstalledWorkflowDisclosureActualEmbeddedGuide(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"skill"}, {"skill", "--json"}, {"skill", "--format", "text"}, {"--format", "log", "skill"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 || stderr != "" {
			t.Fatal(args, code, stderr)
		}
		introduction, _, ok := strings.Cut(out, "## Privacy")
		if !ok {
			t.Fatal("guide privacy boundary was removed")
		}
		requireDeferredWorkflowMeaning(t, introduction)
		if out != skillText(t) {
			t.Fatal("installed guide no longer equals embedded source")
		}
		if n := strings.Count(strings.TrimSuffix(out, "\n"), "\n") + 1; n > 60 {
			t.Fatal("guide exceeded the existing line budget", n)
		}
	}
}

func TestInstalledWorkflowDisclosureManifestPropagation(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("metadata", "--json")
	if code != 0 || stderr != "" {
		t.Fatal(code, stderr)
	}
	description := decode(t, out)["description"].(string)
	requireDeferredWorkflowMeaning(t, description)
	if description != appDescription {
		t.Fatal("manifest diverged from actual root description")
	}
}
