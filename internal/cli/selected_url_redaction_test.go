package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

type selectedURLFixturePage struct {
	engageFixturePage
	result engagecontent.Result
}

func (p *selectedURLFixturePage) Eval(ctx context.Context, expr string, out any) error {
	if !strings.Contains(expr, ".stop()") {
		return p.engageFixturePage.Eval(ctx, expr, out)
	}
	raw := map[string]any{
		"State": p.result.State, "Generation": "synthetic-generation",
		"Account": p.result.Account, "AccountEvidence": []engagecontent.SourceAccount{p.result.Account},
		"Threads": p.result.Threads,
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func TestSelectedUnsafeBodyPublication(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &selectedURLFixturePage{}
	engageControlled(t, &p.engageFixturePage)
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
	safe := "https://example.test/it's-safe?view=1"
	p.result.Threads[0].Blocks = []string{"https://example.test/it's-a-file?tempauth=syntheticreviewsecret", safe}
	p.result.Threads[0].Selected.Messages[0].Blocks = p.result.Threads[0].Blocks
	if code, _, stderr := e.run("content", "fetch", "engage-thread", target); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{
		{"content", "show", "engage-observation:1"},
		{"content", "search", "syntheticreviewsecret"},
		{"search", "syntheticreviewsecret", "--source", "content"},
		{"--format", "text", "content", "show", "engage-observation:1"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(args, stderr)
		}
		if strings.Contains(out, "syntheticreviewsecret") {
			t.Errorf("unsafe retained body returned by %v", args)
		}
	}
	for _, args := range [][]string{
		{"content", "show", "engage-observation:1"},
		{"content", "search", "replysearchneedle"},
		{"search", "replysearchneedle", "--source", "content"},
		{"--format", "text", "content", "show", "engage-observation:1"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 || !strings.Contains(out, safe) || !strings.Contains(out, "replysearchneedle") {
			t.Fatal("safe link and useful reply evidence must remain available", args, out, stderr)
		}
	}
}

func TestSelectedUnsafeRetainedPublicationRefused(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	old := observeEngageThread
	t.Cleanup(func() { observeEngageThread = old })
	observeEngageThread = func(_ context.Context, _ engagecontent.Driver, target engagecontent.ThreadTarget) (engagecontent.Result, error) {
		r := selectedCLIResult(target)
		r.Threads[0].Blocks = []string{"https://example.test/it's-a-file?tempauth=syntheticreviewsecret"}
		r.Threads[0].Selected.Messages[0].Blocks = r.Threads[0].Blocks
		return r, nil
	}
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target := "https://engage.cloud.microsoft/main/threads/" + id
	if code, _, _ := e.run("content", "fetch", "engage-thread", target); code == 0 {
		t.Fatal("unsafe already-decoded evidence must not be published")
	}
	if code, _, _ := e.run("content", "show", "engage-observation:1"); code == 0 {
		t.Fatal("refused evidence reached the archive")
	}
}
