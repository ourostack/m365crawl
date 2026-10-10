package sharepointcontent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFetchNativeTranscriptOffsetsAndContentStates(t *testing.T) {
	request := Request{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"}
	raw := nativeResult()
	raw.State = "transcript_observations"
	raw.File.Path = "/sites/sample/Videos/Video.mp4"
	raw.Transcript = &Transcript{ID: "transcript", Entries: []TranscriptEntry{
		{Ordinal: 0, ID: "first", Text: "First", StartRaw: stringPointer("00:00:01.125"), EndRaw: stringPointer("00:00:02")},
		{Ordinal: 1, ID: "second", Text: "Second", SpeakerDisplayName: stringPointer("Native speaker"), StartRaw: stringPointer("invalid"), EndRaw: stringPointer("999999999999999999:00:00")},
		{Ordinal: 2, ID: "third", Text: "Third", StartRaw: stringPointer("00:00:03"), EndRaw: stringPointer("00:00:02")},
	}}
	page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}
	got, err := Fetch(context.Background(), page, request)
	if err != nil || got.Transcript == nil || len(got.Transcript.Entries) != 3 || !got.Partial {
		t.Fatalf("native transcript refused: %#v,%v", got, err)
	}
	first, second, third := got.Transcript.Entries[0], got.Transcript.Entries[1], got.Transcript.Entries[2]
	if first.StartMS == nil || *first.StartMS != 1125 || first.EndMS == nil || *first.EndMS != 2000 || first.SpeakerDisplayName != nil ||
		second.StartMS != nil || second.EndMS != nil || *second.StartRaw != "invalid" || third.StartMS != nil || third.EndMS != nil {
		t.Fatalf("offsets/speaker inferred or overflowed: %#v", got.Transcript)
	}
	for _, state := range []string{"no_access", "not_found", "timeout", "no_transcript", "transcript_selection_required", "collection_incomplete", "unsupported_download_host"} {
		test := raw
		test.State = state
		test.Transcript = nil
		switch state {
		case "no_access":
			test.HTTPStatus = 403
		case "not_found":
			test.HTTPStatus = 404
		default:
			test.HTTPStatus = 200
		}
		page.result = test
		if got, err := Fetch(context.Background(), page, request); err != nil || got.State != state || got.Transcript != nil || got.File.FileID == "" {
			t.Fatalf("verified metadata content state lost: %s %#v,%v", state, got, err)
		}
	}
}

func TestTranscriptIndependentRetainedCaps(t *testing.T) {
	request := Request{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"}
	makeRaw := func() scriptResult {
		raw := nativeResult()
		raw.State = "transcript_observations"
		raw.File.Path = "/sites/sample/Videos/Video.mp4"
		raw.Transcript = &Transcript{ID: "transcript"}
		return raw
	}
	for _, extra := range []int{0, 1} {
		raw := makeRaw()
		for i := 0; i < 4; i++ {
			raw.Transcript.Entries = append(raw.Transcript.Entries, TranscriptEntry{Ordinal: i, ID: "entry", Text: strings.Repeat("x", 262144)})
		}
		if extra == 1 {
			raw.Transcript.Entries = append(raw.Transcript.Entries, TranscriptEntry{Ordinal: 4, ID: "entry", Text: "x"})
		}
		page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}
		got, err := Fetch(context.Background(), page, request)
		if extra == 0 {
			if err != nil || len(got.Transcript.Entries) != 4 {
				t.Fatalf("inclusive transcript text cap refused: %#v,%v", got, err)
			}
		} else {
			var safe *ReadError
			if !errors.As(err, &safe) || safe.Code != "too_large" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("transcript aggregate overrun published: %#v,%v", got, err)
			}
		}
	}
	for _, field := range []string{"text", "id", "speaker", "start", "end"} {
		raw := makeRaw()
		entry := TranscriptEntry{ID: "entry", Text: "text"}
		large := strings.Repeat("x", 262145)
		switch field {
		case "text":
			entry.Text = large
		case "id":
			entry.ID = large
		case "speaker":
			entry.SpeakerDisplayName = &large
		case "start":
			entry.StartRaw = &large
		case "end":
			entry.EndRaw = &large
		}
		raw.Transcript.Entries = []TranscriptEntry{entry}
		page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}
		got, err := Fetch(context.Background(), page, request)
		var safe *ReadError
		if !errors.As(err, &safe) || safe.Code != "too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s retained cap missed: %#v,%v", field, got, err)
		}
	}
}
