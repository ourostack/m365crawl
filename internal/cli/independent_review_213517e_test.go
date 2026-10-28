package cli

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func TestIndependentReviewUnsafeBodyPublication(t *testing.T) {
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
}
