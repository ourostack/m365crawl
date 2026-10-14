package cli

import (
	"context"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"reflect"
	"testing"
)

func TestEngageImmutableCLIHistoryAndMetadata(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	r := engageCLIResult("A")
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) { return r, nil }
	fetch := func() map[string]any {
		t.Helper()
		code, out, stderr := e.run("content", "fetch", "engage-home")
		if code != 0 {
			t.Fatal(stderr)
		}
		return decode(t, out)
	}
	first := fetch()
	code, out, stderr := e.run("content", "show", "engage-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	original := decode(t, out)["items"].([]any)[0]
	title := "Synthetic change"
	r.Threads[0].Title = &title
	fetch()
	r = engageCLIResult("A")
	repeat := fetch()
	if repeat["reused"] != true || repeat["session_id"] != first["session_id"] {
		t.Fatal("A-B-A did not reuse")
	}
	code, out, stderr = e.run("content", "show", "engage-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !reflect.DeepEqual(original, decode(t, out)["items"].([]any)[0]) {
		t.Fatal("A-B-A changed original evidence")
	}
	r.State = "metadata_only"
	r.Threads = nil
	r.Losses = []engagecontent.Loss{{Code: "native_content_not_observed", Count: 1}}
	meta := fetch()
	if meta["session_id"] == nil || len(meta["observation_ids"].([]any)) != 0 || meta["state"] != "metadata_only" {
		t.Fatal("metadata invented observations")
	}
	code, out, stderr = e.run("content", "show", "engage-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, out)
	item := doc["items"].([]any)[0].(map[string]any)
	if item["retained"] != true || !reflect.DeepEqual(item["session_losses"], original.(map[string]any)["session_losses"]) {
		t.Fatal("metadata lost own retained evidence")
	}
	cov := doc["engage_content_coverage"].(map[string]any)
	current := cov["instances"].([]any)[0].(map[string]any)["current_session"].(map[string]any)
	if current["state"] != "metadata_only" || current["blocks_total"] != float64(0) {
		t.Fatal("coverage borrowed retained body")
	}
	r = engageCLIResult("A")
	r.Threads[0].Blocks = []string{""}
	empty := fetch()
	if empty["state"] != "observations" || len(empty["observation_ids"].([]any)) != 1 {
		t.Fatal("empty admitted block became metadata-only")
	}
}
