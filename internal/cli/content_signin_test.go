package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestContentSigninExplicitAgreement(t *testing.T) {
	e := cloudCLIEnv(t)
	page := newTrPage()
	_, opts := fakeBrowser(t, page)
	signinTTY(t, false, "")
	url := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	if code, _, stderr := e.run("content", "signin", url); code == 0 {
		t.Fatal("visible window without agreement")
	} else if fix, _ := errorOf(t, stderr)["fix"].(string); !strings.Contains(fix, "content signin "+url) || strings.Contains(fix, "transcripts signin") {
		t.Fatalf("wrong explicit sign-in route: %s", fix)
	}

	if code, out, stderr := e.run("content", "signin", url, "--user-agreed"); code != 0 || !decode(t, out)["signed_in"].(bool) {
		t.Fatalf("signin: %s", stderr)
	}
	if opts.DisposableFixture {
		t.Fatal("product sign-in uses fixture keychain")
	}
	if code, _, _ := e.run("content", "signin", "http://invalid", "--user-agreed"); code == 0 {
		t.Fatal("invalid signin root")
	}
}

func TestContentSigninFailureAndTerminalCleanup(t *testing.T) {
	e := cloudCLIEnv(t)
	url := "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"
	for _, args := range [][]string{
		{"--account", "t/u", "content", "signin", url, "--user-agreed"},
		{"content", "signin", url, "--user-agreed", "--browser", "/missing-browser"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("bad signin accepted")
		}

	}
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	for _, mode := range []string{"open", "close", "panic"} {
		closer := &cloudFixtureCloser{}
		if mode == "close" {
			closer.err = errors.New("private close")
		}
		openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
			if mode == "open" {
				return nil, nil, errors.New("synthetic open")
			}
			if mode == "panic" {
				return &cloudFixturePage{host: "fixture.sharepoint.com", panicEval: true}, closer, nil
			}
			page := newTrPage()
			page.host = "fixture.sharepoint.com"
			return page, closer, nil
		}
		if code, _, _ := e.run("content", "signin", url, "--user-agreed"); code == 0 {
			t.Fatal("signin terminal failure")
		}
		if mode != "open" && closer.n != 1 {
			t.Fatal("signin owner not closed")
		}
	}
}

func TestContentSigninCancellationDuringClose(t *testing.T) {
	e := cloudCLIEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	closed := 0
	page := newTrPage()
	page.host = "fixture.sharepoint.com"
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return page, closerFunc(func() error {
			closed++
			cancel()
			return nil
		}), nil
	}

	rt := &runtime{ctx: ctx, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
	result, err := rt.signinWindow("", browser.KindEdge, "fixture.sharepoint.com", "/sites/sample")
	if !errors.Is(err, context.Canceled) || result != nil || closed != 1 {
		t.Fatalf("terminal cancellation: result=%v err=%v closes=%d", result, err, closed)
	}
}

func TestContentSigninErrorPreservesSharedBoundary(t *testing.T) {
	original := errs.SigninNeedsAgreement()
	before := original.Fix
	err := contentSigninError(original, "m365crawl content signin URL")
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != original.Code || coded.Exit != original.Exit || !strings.Contains(coded.Fix, "content signin URL") || original.Fix != before {
		t.Fatal("content error changed shared code or original transcript error")
	}
	if !errors.Is(contentSigninError(context.Canceled, ""), context.Canceled) || contentSigninError(nil, "") != nil {
		t.Fatal("content error masked cancellation or success")
	}
}
