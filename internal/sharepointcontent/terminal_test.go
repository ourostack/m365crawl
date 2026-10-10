package sharepointcontent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRequestRejectedPathsAndHostLabels(t *testing.T) {
	for _, req := range []Request{
		{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx?id=/sites/sample/video.mp4"},
		{Kind: "page", URL: "https://fix_ture.sharepoint.com/sites/sample/SitePages/Page.aspx"},
		{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx?bad=%GG"},
	} {
		if got, err := admit(req); err == nil || got != (admittedRequest{}) {
			t.Fatalf("invalid native request admitted: %#v,%v", got, err)
		}
	}
}

func TestFetchEntryAndHostCancellationHavePrecedence(t *testing.T) {
	req := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: nativeResult()}
	if got, err := Fetch(context.Background(), page, Request{}); err == nil || !reflect.DeepEqual(got, Result{}) || len(page.calls) != 0 {
		t.Fatalf("invalid Fetch reached page: %#v,%v", got, err)
	}
	parent, cancel := context.WithCancel(context.Background())
	ctx := &publicationContext{Context: parent, cancel: cancel, at: 1}
	if got, err := land(ctx, page, req); !errors.Is(err, context.Canceled) || got != (admittedRequest{}) {
		t.Fatalf("entry cancellation lost: %#v,%v", got, err)
	}
	cancel()
}

func TestMetadataOptionalPointersAndRawTimeRemainOwned(t *testing.T) {
	req := admittedRequest{Kind: "page", Host: "fixture.sharepoint.com", Path: "/sites/sample/SitePages/Page.aspx"}
	raw := nativeResult()
	list, length := int64(7), int64(123)
	raw.File.ListItemID = &list
	raw.File.Length = &length
	raw.File.ModifiedRaw = stringPointer("unknown native date")
	got, err := decode(context.Background(), req, raw)
	if err != nil || got.File.ModifiedAt != nil || len(got.Losses) != 1 || got.Losses[0].Code != "modified_time_unmapped" {
		t.Fatalf("raw unknown date fabricated: %#v,%v", got, err)
	}
	list, length = 8, 456
	if *got.File.ListItemID != 7 || *got.File.Length != 123 {
		t.Fatal("metadata pointers retained caller alias")
	}
	raw.Losses = []Loss{{Code: "modified_time_unmapped", Count: 1}}
	if _, err := decode(context.Background(), req, raw); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"signin_required", "elsewhere", "failed", "malformed", "identity_mismatch", "too_large", "unexpected_response"} {
		if got, err := decode(context.Background(), req, scriptResult{State: state}); err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s fatal output published: %#v,%v", state, got, err)
		}
	}
}

func TestIndependentTotalStringsExactBoundary(t *testing.T) {
	req := admittedRequest{Kind: "stream", Host: "fixture.sharepoint.com", Path: "/sites/sample/SitePages/Page.aspx"}
	for _, extra := range []int{0, 1} {
		raw := nativeResult()
		raw.State = "transcript_observations"
		raw.Transcript = &Transcript{ID: "t"}
		retained := len(raw.Account.Host) + len(raw.Account.WebID) + len(raw.Account.LoginName) + len(raw.File.SiteID) + len(raw.File.WebID) + len(raw.File.FileID) + len(raw.File.Path) + len(*raw.File.ModifiedRaw) + 1
		for i := 0; i < 32; i++ {
			raw.Transcript.Entries = append(raw.Transcript.Entries, TranscriptEntry{Ordinal: i, ID: "e", StartRaw: stringPointer(strings.Repeat("x", 262144))})
			retained += 262145
		}
		last := raw.Transcript.Entries[31].StartRaw
		*last = (*last)[:len(*last)-(retained-(8<<20))+extra]
		got, err := decode(context.Background(), req, raw)
		if extra == 0 {
			if err != nil || got.Transcript == nil {
				t.Fatalf("inclusive structural cap refused: %#v,%v", got, err)
			}
		} else {
			var safe *ReadError
			if !errors.As(err, &safe) || safe.Code != "too_large" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("structural cap+1 published: %#v,%v", got, err)
			}
		}
	}
}
