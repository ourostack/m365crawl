package sharepointcontent

import (
	"context"
	"testing"
)

func TestNativeLossesUseDeterministicCodeOrder(t *testing.T) {
	raw := nativeResult()
	raw.State = "transcript_observations"
	raw.Transcript = &Transcript{ID: "transcript", Entries: []TranscriptEntry{
		{Ordinal: 0, ID: "first", Text: "text"},
		{Ordinal: 1, ID: "second", Text: "text", StartRaw: stringPointer("00:00:02"), EndRaw: stringPointer("00:00:01")},
	}}
	result, err := decode(context.Background(), admittedRequest{Kind: "stream", Host: raw.Account.Host, Path: raw.File.Path}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Losses) != 2 || result.Losses[0].Code != "transcript_offset_reversed" || result.Losses[1].Code != "transcript_offset_unmapped" {
		t.Fatalf("loss code order depends on first source encounter: %#v", result.Losses)
	}
}
