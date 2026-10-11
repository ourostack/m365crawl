package cli

import (
	"slices"
	"testing"
)

func TestMetadataExposesNotesCaptureAndOfflineFollowUp(t *testing.T) {
	m := manifest()
	for _, command := range []string{"notes sync", "notes list", "notes search", "notes show", "notes sources"} {
		c, found := m.Commands[command]
		if !found || !c.JSON || c.Mutates != (command == "notes sync") {
			t.Fatalf("notes command missing or misclassified: %s/%#v", command, c)
		}
	}
	if !slices.Contains(m.Capabilities, "notes") || !slices.Contains(m.Privacy.LocalOnlyScopes, "onenote_index") {
		t.Fatal("tool discovery omits local indexed notes capability")
	}
}
