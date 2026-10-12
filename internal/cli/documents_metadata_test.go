package cli

import (
	"slices"
	"testing"
)

func TestDocumentsMetadataDiscoversCommandsAndLocalScope(t *testing.T) {
	m := manifest()
	for _, name := range []string{"docs sync", "docs list", "docs search", "docs show", "docs sources"} {
		command, ok := m.Commands[name]
		if !ok || !command.JSON || command.Mutates != (name == "docs sync") {
			t.Fatalf("command discovery lost route or explicit-write contract: %s", name)
		}
	}
	if !slices.Contains(m.Capabilities, "documents") || !slices.Contains(m.Privacy.LocalOnlyScopes, "office_json_metadata") {
		t.Fatal("tool discovery omits local document metadata capability")
	}
}
