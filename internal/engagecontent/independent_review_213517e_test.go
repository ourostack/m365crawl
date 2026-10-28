package engagecontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestIndependentReviewWholeBodyURLRedaction(t *testing.T) {
	target, raw := selectedFixture(t)
	var selected SelectedThread
	if err := json.Unmarshal(raw.Threads[0].Selected, &selected); err != nil {
		t.Fatal(err)
	}
	unsafe := "https://example.test/it's-a-file?tempauth=synthetic-private"
	if SafeThreadLink(unsafe) {
		t.Fatal("whole URL unexpectedly considered safe")
	}
	selected.Messages[0].Blocks = []string{"before " + unsafe + " after"}
	raw.Threads[0].Blocks = selected.Messages[0].Blocks
	raw.Threads[0].Selected = rawValue(selected)
	got, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private") {
		t.Fatalf("unsafe body URL retained after decode and ValidateResult: %s", got.Threads[0].Selected.Messages[0].Blocks[0])
	}
}
