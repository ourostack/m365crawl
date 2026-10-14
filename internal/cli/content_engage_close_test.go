package cli

import (
	"context"
	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/transcripts"
	"io"
	"testing"
	"time"
)

type engagePanicCloser struct{}

func TestEngageMissingCloserRefuses(t *testing.T) {
	if err := closeEngageBrowser(nil); err == nil {
		t.Fatal("nil closer accepted")
	}
}

func (*engagePanicCloser) Close() error { panic("synthetic close") }

func TestEngageTerminalClosePanicDiscards(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	engageControlled(t, p)
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult("A"), nil
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return p, &engagePanicCloser{}, nil
	}
	r, err := captureEngage(context.Background(), browser.LaunchOptions{})
	if err == nil || r.Account.Host != "" {
		t.Fatal("close panic published")
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
	s, err := rt.signinEngage(browser.LaunchOptions{})
	if err == nil || s.AuthState != "" {
		t.Fatal("signin close panic published")
	}
}

func TestEngageRunFlagsBeforeRegistration(t *testing.T) {
	cmd := &contentFetchEngageHomeCmd{Timeout: time.Minute}
	for _, g := range []*Globals{{Account: "t/u"}, {Fields: "unsupported"}} {
		rt := &runtime{ctx: context.Background(), g: g}
		if g.Fields != "" {
			rt.fields = []string{g.Fields}
		}
		if err := cmd.Run(rt); err == nil {
			t.Fatal("invalid flags admitted")
		}
	}
}

func TestEngageFetchCancelledContextAtTerminal(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		cancel()
		return engageCLIResult("A"), nil
	}
	rt := &runtime{ctx: ctx, g: &Globals{}, dbPath: e.db}
	r, err := rt.fetchEngage(browser.LaunchOptions{})
	if err == nil || r.SourceID != nil {
		t.Fatal("cancelled acquisition published")
	}
}
