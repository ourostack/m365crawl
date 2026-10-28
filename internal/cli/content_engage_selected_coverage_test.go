package cli

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestSelectedCLIAdmissionAndDiscardedResults(t *testing.T) {
	command := contentFetchEngageThreadCmd{}
	if err := command.Run(&runtime{g: &Globals{}, fields: []string{"invalid"}}); err == nil {
		t.Fatal("invalid fetch fields admitted")
	}
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	closer := engageControlled(t, p)
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	url := "https://engage.cloud.microsoft/main/threads/" + id
	for _, args := range [][]string{
		{"--account", "t/u", "content", "fetch", "engage-thread", url},
		{"--fields", "not-a-field", "content", "fetch", "engage-thread", url},
		{"content", "fetch", "engage-thread", url, "--timeout", "0"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("invalid command admitted")
		}
	}
	if closer.n != 0 {
		t.Fatal("admission failure launched browser")
	}
	old := observeEngageThread
	t.Cleanup(func() { observeEngageThread = old })
	observeEngageThread = func(context.Context, engagecontent.Driver, engagecontent.ThreadTarget) (engagecontent.Result, error) {
		return engageCLIResult("viewer"), nil
	}
	if code, _, _ := e.run("content", "fetch", "engage-thread", url); code == 0 {
		t.Fatal("unbound selection admitted")
	}
}

func TestSelectedPreviewSignalsMetadataClipping(t *testing.T) {
	target, _ := engagecontent.ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`)))
	r := selectedCLIResult(target)
	r.Threads[0].Selected.Messages[0].Blocks = []string{"x"}
	r.Threads[0].Selected.Messages[1].Blocks = []string{"y"}
	r.Threads[0].Blocks = []string{"x"}
	item := shapeEngage(store.EngageObservationRow{Thread: r.Threads[0]}, 2)
	if !item.TextTruncated {
		t.Fatal("selected author/attachment clipping not disclosed")
	}
	if *r.Threads[0].Selected.Messages[0].Author.Name != "Synthetic Author" {
		t.Fatal("preview mutated retained evidence")
	}
}
