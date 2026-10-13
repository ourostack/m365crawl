package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

type cloudFixturePage struct {
	raw       string
	host      string
	panicEval bool
	cancel    context.CancelFunc
}

func (p *cloudFixturePage) Navigate(_ context.Context, _ string) error { return nil }
func (p *cloudFixturePage) Host(_ context.Context) (string, error)     { return p.host, nil }
func (p *cloudFixturePage) Eval(_ context.Context, _ string, out any) error {
	if p.panicEval {
		panic("private")
	}
	if p.cancel != nil {
		p.cancel()
	}
	return json.Unmarshal([]byte(p.raw), out)
}

const cloudFixtureRaw = `{"state":"page_observations","status":200,"account":{"Host":"fixture.sharepoint.com","WebID":"cccccccc-cccc-cccc-cccc-cccccccccccc","LoginName":"synthetic","ID":11},"file":{"SiteID":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","WebID":"cccccccc-cccc-cccc-cccc-cccccccccccc","FileID":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","Path":"/sites/sample/SitePages/Page.aspx","Title":"Synthetic page"},"pageControls":[{"Ordinal":0,"Type":4,"State":"text","Texts":["searchableword"]}]}`

type cloudFixtureCloser struct {
	n   int
	err error
}

func (c *cloudFixtureCloser) Close() error { c.n++; return c.err }

type cloudTabless struct {
	n                    int
	panicPage, failClose bool
}

func (b *cloudTabless) Page(context.Context) (*browser.Page, error) {
	if b.panicPage {
		panic("private tab")
	}
	return nil, errors.New("private tab")
}
func (b *cloudTabless) Close() error {
	b.n++
	if b.failClose {
		return errors.New("private close")
	}
	return nil
}
func TestContentCaptureTabCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := launchBrowser
	t.Cleanup(func() { launchBrowser = saved })
	for _, b := range []*cloudTabless{{failClose: true}, {panicPage: true}, {panicPage: true, failClose: true}} {
		launchBrowser = func(context.Context, browser.LaunchOptions) (launchedBrowser, error) { return b, nil }
		_, _, err := openBrowser(context.Background(), browser.LaunchOptions{Profile: t.TempDir(), Headless: true})
		if err == nil || b.n != 1 {
			t.Fatal("tab failure did not close exact owner")
		}
	}
}

func TestContentBrowserCloseBeforePublication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := openBrowser
	t.Cleanup(func() { openBrowser = saved })
	request, err := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "close", "cancel", "panic", "wrong-host"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			closer := &cloudFixtureCloser{}
			page := &cloudFixturePage{raw: cloudFixtureRaw, host: "fixture.sharepoint.com"}
			switch mode {
			case "close":
				closer.err = errors.New("private close")
			case "cancel":
				page.cancel = cancel
			case "panic":
				page.panicEval = true
			case "wrong-host":
				page.host = "elsewhere.invalid"
			}
			openBrowser = func(_ context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
				if !o.Headless {
					t.Fatal("capture not headless")
				}
				return page, closer, nil
			}
			got, err := captureCloud(ctx, browser.LaunchOptions{Profile: t.TempDir()}, request)
			if closer.n != 1 {
				t.Fatalf("closed %d times", closer.n)
			}
			if mode == "success" {
				if err != nil || len(got.PageControls) != 1 {
					t.Fatalf("capture: %v", err)
				}
			} else if err == nil || got.Kind != "" {
				t.Fatal("terminal boundary published staged data")
			}
		})
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		t.Fatal("invalid request launched")
		return nil, nil, nil
	}
	if _, err := captureCloud(context.Background(), browser.LaunchOptions{}, sharepointcontent.CanonicalRequest{}); err == nil {
		t.Fatal("invalid admitted")
	}
}
