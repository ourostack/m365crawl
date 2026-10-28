package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

type selectedLandingPage struct {
	selectedURLFixturePage
}

func (p *selectedLandingPage) Navigate(_ context.Context, target string) error {
	p.events = append(p.events, "navigate:"+target)
	return &selectedLandingError{p}
}

type selectedLandingError struct{ page *selectedLandingPage }

func (*selectedLandingError) Error() string { return "browser navigation was superseded" }
func (e *selectedLandingError) EvalReplacementDocument(ctx context.Context, expr string, out any) error {
	return e.page.Eval(ctx, expr, out)
}

func TestSelectedLandingCloseBarrierAndOfflineConsumers(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &selectedLandingPage{}
	closer := engageControlled(t, &p.engageFixturePage)
	controlled := openBrowser
	openBrowser = func(ctx context.Context, options browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		_, closer, err := controlled(ctx, options)
		return p, closer, err
	}
	old := observeEngageThread
	t.Cleanup(func() { observeEngageThread = old })
	observeEngageThread = engagecontent.ObserveThread
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target := "https://engage.cloud.microsoft/main/threads/" + id
	selection, err := engagecontent.ParseThreadURL(target)
	if err != nil {
		t.Fatal(err)
	}
	p.result = selectedCLIResult(selection)
	closer.err = errors.New("synthetic terminal Close failed")
	if code, _, _ := e.run("content", "fetch", "engage-thread", target); code == 0 {
		t.Fatal("replacement result published before successful terminal Close")
	}
	if code, _, _ := e.run("content", "show", "engage-observation:1"); code == 0 {
		t.Fatal("Close failure published staged content")
	}
	closer.err = nil
	if code, _, stderr := e.run("content", "fetch", "engage-thread", target); code != 0 {
		t.Fatal(stderr)
	}
	before := len(p.events)
	for _, args := range [][]string{
		{"content", "show", "engage-observation:1"},
		{"content", "search", "replysearchneedle"},
		{"search", "replysearchneedle", "--source", "content"},
		{"--format", "text", "content", "show", "engage-observation:1"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 || !strings.Contains(out, "replysearchneedle") {
			t.Fatal("fresh selected evidence lost in offline consumer", args, out, stderr)
		}
	}
	if len(p.events) != before || closer.n != 2 {
		t.Fatal("offline consumers acquired the browser or terminal Close was omitted")
	}
}
