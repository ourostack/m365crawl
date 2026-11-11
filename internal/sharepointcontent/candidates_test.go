package sharepointcontent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTranscriptSelectionCandidates(t *testing.T) {
	request := Request{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"}
	makeRaw := func(state string, candidates ...TranscriptCandidate) scriptResult {
		raw := nativeResult()
		raw.State = state
		raw.File.Path = "/sites/sample/Videos/Video.mp4"
		raw.Candidates = candidates
		return raw
	}
	fetch := func(raw scriptResult) (Result, error) {
		return Fetch(context.Background(), &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}, request)
	}
	label := "English"
	raw := makeRaw("transcript_selection_required", TranscriptCandidate{ID: "first", DisplayName: &label, LanguageTag: stringPointer("en-US")}, TranscriptCandidate{ID: "second"})
	got, err := fetch(raw)
	want := []TranscriptCandidate{{ID: "first", DisplayName: stringPointer("English"), LanguageTag: stringPointer("en-US")}, {ID: "second"}}
	if err != nil || got.State != "transcript_selection_required" || got.Transcript != nil || !reflect.DeepEqual(got.TranscriptCandidates, want) {
		t.Fatalf("selection candidates lost: %#v,%v", got, err)
	}
	label = "mutated"
	if *got.TranscriptCandidates[0].DisplayName != "English" {
		t.Fatal("candidate label aliases the script result")
	}
	canonical, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if validated, err := ValidateResult(context.Background(), canonical, got); err != nil || !reflect.DeepEqual(validated, got) {
		t.Fatalf("stored selection candidates refused: %#v,%v", validated, err)
	}
	legacy, err := fetch(makeRaw("transcript_selection_required"))
	if err != nil || legacy.TranscriptCandidates != nil {
		t.Fatalf("observation archived before candidates refused: %#v,%v", legacy, err)
	}
	if validated, err := ValidateResult(context.Background(), canonical, legacy); err != nil || !reflect.DeepEqual(validated, legacy) {
		t.Fatalf("legacy selection observation no longer validates: %#v,%v", validated, err)
	}
	many := make([]TranscriptCandidate, 129)
	for i := range many {
		many[i] = TranscriptCandidate{ID: fmt.Sprint("t", i)}
	}
	limit := strings.Repeat("x", 256<<10)
	heavy := make([]TranscriptCandidate, 40)
	for i := range heavy {
		heavy[i] = TranscriptCandidate{ID: fmt.Sprint("h", i), DisplayName: &limit}
	}
	nul := "bad\x00label"
	large := strings.Repeat("x", 262145)
	for name, test := range map[string]struct {
		raw  scriptResult
		code string
	}{
		"other state":   {makeRaw("no_transcript", TranscriptCandidate{ID: "a"}, TranscriptCandidate{ID: "b"}), "malformed"},
		"one":           {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a"}), "malformed"},
		"too many":      {makeRaw("transcript_selection_required", many...), "malformed"},
		"duplicate":     {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a"}, TranscriptCandidate{ID: "a"}), "malformed"},
		"empty id":      {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a"}, TranscriptCandidate{}), "malformed"},
		"nul":           {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a", DisplayName: &nul}, TranscriptCandidate{ID: "b"}), "malformed"},
		"invalid utf-8": {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a\xff"}, TranscriptCandidate{ID: "b"}), "malformed"},
		"aggregate":     {makeRaw("transcript_selection_required", heavy...), "too_large"},
		"large":         {makeRaw("transcript_selection_required", TranscriptCandidate{ID: "a", LanguageTag: &large}, TranscriptCandidate{ID: "b"}), "too_large"},
	} {
		got, err := fetch(test.raw)
		var safe *ReadError
		if !errors.As(err, &safe) || safe.Code != test.code || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s: unsafe candidates published: %#v,%v", name, got, err)
		}
	}
}
