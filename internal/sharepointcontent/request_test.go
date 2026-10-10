package sharepointcontent

import (
	"strings"
	"testing"
)

func TestRequestAdmission(t *testing.T) {
	page := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	video := "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"
	for _, test := range []struct {
		name, kind, url, transcript, site, path string
		valid                                   bool
	}{
		{"page", "page", page, "", "/sites/sample", "/sites/sample/SitePages/Page.aspx", true},
		{"page-query", "page", page + "?web=1&tracking=ignored", "", "/sites/sample", "/sites/sample/SitePages/Page.aspx", true},
		{"teams-page", "page", strings.Replace(page, "/sites/", "/teams/", 1), "", "/teams/sample", "/teams/sample/SitePages/Page.aspx", true},
		{"encoded", "page", strings.Replace(page, "Page.aspx", "A%20B%27%23%25%E2%98%83.aspx", 1), "", "/sites/sample", "/sites/sample/SitePages/A B'#%\u2603.aspx", true},
		{"once-only", "page", strings.Replace(page, "Page.aspx", "%252F.aspx", 1), "", "/sites/sample", "/sites/sample/SitePages/%2F.aspx", true},
		{"video", "stream", video, "native-transcript", "/sites/sample", "/sites/sample/Videos/Video.mp4", true},
		{"video-query", "stream", video + "&referrer=ignored", "", "/sites/sample", "/sites/sample/Videos/Video.mp4", true},
		{"alternate-host", "page", strings.Replace(page, ".sharepoint.com", ".sharepoint-df.com", 1), "", "/sites/sample", "/sites/sample/SitePages/Page.aspx", true},
		{"port443", "page", strings.Replace(page, ".com/", ".com:443/", 1), "", "/sites/sample", "/sites/sample/SitePages/Page.aspx", true},
		{"unknown-kind", "news", page, "", "", "", false},
		{"http", "page", strings.Replace(page, "https:", "http:", 1), "", "", "", false},
		{"userinfo", "page", strings.Replace(page, "fixture.", "user@fixture.", 1), "", "", "", false},
		{"port", "page", strings.Replace(page, ".com/", ".com:8443/", 1), "", "", "", false},
		{"fragment", "page", page + "#part", "", "", "", false},
		{"bare-suffix", "page", strings.Replace(page, "fixture.", "", 1), "", "", "", false},
		{"lookalike", "page", strings.Replace(page, ".com/", ".com.invalid/", 1), "", "", "", false},
		{"invalid-label", "page", strings.Replace(page, "fixture.", "-fixture.", 1), "", "", "", false},
		{"ip", "page", strings.Replace(page, "fixture.sharepoint.com", "127.0.0.1", 1), "", "", "", false},
		{"personal", "page", strings.Replace(page, "/sites/", "/personal/", 1), "", "", "", false},
		{"nested-site", "page", strings.Replace(page, "/sample/", "/sample/subsite/", 1), "", "", "", false},
		{"dot", "page", strings.Replace(page, "/sample/", "/../", 1), "", "", "", false},
		{"escaped-separator", "page", strings.Replace(page, "Page.aspx", "A%2FPage.aspx", 1), "", "", "", false},
		{"backslash", "page", strings.Replace(page, "Page.aspx", "A%5CPage.aspx", 1), "", "", "", false},
		{"nul", "page", strings.Replace(page, "Page.aspx", "A%00Page.aspx", 1), "", "", "", false},
		{"wrong-page", "page", strings.Replace(page, ".aspx", ".html", 1), "", "", "", false},
		{"page-transcript", "page", page, "unrelated", "", "", false},
		{"missing-video-id", "stream", strings.Split(video, "?")[0], "", "", "", false},
		{"duplicate-video-id", "stream", video + "&id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4", "", "", "", false},
		{"out-of-site", "stream", strings.Replace(video, "id=%2Fsites%2Fsample", "id=%2Fsites%2Fother", 1), "", "", "", false},
		{"video-dot", "stream", strings.Replace(video, "%2FVideos%2F", "%2F..%2F", 1), "", "", "", false},
		{"malformed-query", "stream", video + "&broken=%GG", "", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := admit(Request{Kind: test.kind, URL: test.url, TranscriptID: test.transcript})
			if !test.valid {
				if err == nil || got != (admittedRequest{}) {
					t.Fatalf("invalid source admitted: %#v,%v", got, err)
				}
				if strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "unrelated") {
					t.Fatalf("source leaked in error: %v", err)
				}
				return
			}
			if err != nil || got.Kind != test.kind || got.Site != test.site || got.Path != test.path || got.TranscriptID != test.transcript {
				t.Fatalf("native request changed: %#v,%v", got, err)
			}
		})
	}
}

func TestRequestRuntimeByteBoundaries(t *testing.T) {
	prefix := "https://fixture.sharepoint.com/sites/sample/SitePages/"
	url := prefix + strings.Repeat("x", 4096-len("/sites/sample/SitePages/")-len(".aspx")) + ".aspx"
	if _, err := admit(Request{Kind: "page", URL: url}); err != nil {
		t.Fatalf("exact decoded path boundary refused: %v", err)
	}
	if _, err := admit(Request{Kind: "page", URL: strings.TrimSuffix(url, ".aspx") + "x.aspx"}); err == nil {
		t.Fatal("decoded path over boundary admitted")
	}
	base := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx?ignored="
	url = base + strings.Repeat("x", 8192-len(base))
	if _, err := admit(Request{Kind: "page", URL: url}); err != nil {
		t.Fatalf("exact URL boundary refused: %v", err)
	}
	if _, err := admit(Request{Kind: "page", URL: url + "x"}); err == nil {
		t.Fatal("URL over boundary admitted")
	}
}
