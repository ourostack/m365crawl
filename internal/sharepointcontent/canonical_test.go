package sharepointcontent

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestCloudResultBoundaryReusesTypedDecoder(t *testing.T) {
	request, err := NormalizeRequest(Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := admit(request.Request)
	result, err := decode(context.Background(), a, nativeResult())
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := ValidateResult(context.Background(), request, result); err != nil || !reflect.DeepEqual(decoded, result) {
		t.Fatal("canonical result changed")
	}
	badRequest := request
	badRequest.LocatorKey = "different"
	if _, err := ValidateResult(context.Background(), badRequest, result); err == nil {
		t.Fatal("bad locator accepted")
	}
	result.Partial = true
	if _, err := ValidateResult(context.Background(), request, result); err == nil {
		t.Fatal("invented partial accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ValidateResult(ctx, request, result); err == nil {
		t.Fatal("cancel accepted")
	}
	rawPage := nativeResult()
	rawPage.File.ModifiedRaw = stringPointer("unknown")
	result, err = decode(context.Background(), a, rawPage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateResult(context.Background(), request, result); err != nil {
		t.Fatal("native modified loss")
	}
	// A selected body's derived offset losses are reconstructed, not accepted as raw loss codes.
	request, err = NormalizeRequest(Request{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ = admit(request.Request)
	raw := nativeResult()
	raw.State = "transcript_observations"
	raw.File.Path = a.Path
	raw.Transcript = &Transcript{ID: "T", Entries: []TranscriptEntry{{Ordinal: 0, ID: "E", Text: "text"}}}
	result, err = decode(context.Background(), a, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateResult(context.Background(), request, result); err != nil {
		t.Fatal("derived loss failed validation")
	}
	result.Losses[0].Count++
	if _, err := ValidateResult(context.Background(), request, result); err == nil {
		t.Fatal("invented loss accepted")
	}
}

func TestCloudSafeRequestRoundTrip(t *testing.T) {
	for _, request := range []Request{
		{Kind: "page", URL: "https://FIXTURE.sharepoint.com:443/sites/sample/SitePages/A%20%27%25%E2%98%83.aspx?ticket=private&web=1"},
		{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?ticket=private&id=%2Fsites%2Fsample%2FVideos%2FA%20%25%E2%98%83.mp4", TranscriptID: "T|[native]"},
		{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/" + strings.Repeat("☃", 1000) + ".aspx"},
	} {
		original, err := admit(request)
		if err != nil {
			t.Fatal(err)
		}
		got, err := NormalizeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		again, err := admit(got.Request)
		if err != nil || again != original {
			t.Fatalf("round trip: %#v %v", again, err)
		}
		if strings.Contains(got.Request.URL, "ticket") || strings.Contains(got.Request.URL, "private") || strings.Contains(got.LocatorKey, "private") {
			t.Fatal("ignored query persisted")
		}
		round, err := NormalizeRequest(got.Request)
		if err != nil || round != got {
			t.Fatal("canonical request changed on retry")
		}
		if _, err := ValidateResult(context.Background(), got, Result{}); err == nil {
			t.Fatal("invalid native result admitted")
		}
	}
	if _, err := NormalizeRequest(Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/P.aspx?bad=%ZZ"}); err == nil {
		t.Fatal("malformed query accepted")
	}
}
