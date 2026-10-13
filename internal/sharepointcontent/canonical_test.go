package sharepointcontent

import (
	"context"
	"strings"
	"testing"
)

func TestCloudSafeRequestRoundTrip(t *testing.T) {
	for _, request := range []Request{
		{Kind: "page", URL: "https://FIXTURE.sharepoint.com:443/sites/sample/SitePages/A%20%27%25%E2%98%83.aspx?ticket=private&web=1"},
		{Kind: "stream", URL: "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?ticket=private&id=%2Fsites%2Fsample%2FVideos%2FA%20%25%E2%98%83.mp4", TranscriptID: "T|[native]"},
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
