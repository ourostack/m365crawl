package officedocuments

import (
	"strings"
	"testing"
)

func TestMetadataTextIndexesNativeValuesWithoutInventedKeys(t *testing.T) {
	document := Document{Title: "Synthetic title", URL: "https://example.invalid/document",
		DriveID: "opaque-drive", Creator: Person{DisplayName: "Synthetic writer", UPN: "writer@example.invalid"},
		Sharing: &Sharing{DisplayName: "Synthetic sharer", Email: "sharer@example.invalid"}}
	text := MetadataText(document)
	for _, value := range []string{"Synthetic title", "https://example.invalid/document", "opaque-drive",
		"Synthetic writer", "writer@example.invalid", "Synthetic sharer", "sharer@example.invalid"} {
		if !strings.Contains(text, value) {
			t.Fatalf("native metadata value omitted: %s", value)
		}
	}
	for _, key := range []string{"Creator", "DriveID", "Sharing", "DisplayName"} {
		if strings.Contains(text, key) {
			t.Fatalf("model property name became searchable native evidence: %s", key)
		}
	}
}
