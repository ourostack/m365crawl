package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func selectedCLIResult(target engagecontent.ThreadTarget) engagecontent.Result {
	r := engageCLIResult("viewer")
	r.Selection = &target
	r.Qualification = engagecontent.SelectedQualification
	r.Threads[0].ID = target.ThreadID
	r.Losses = []engagecontent.Loss{{Code: "clock_unmapped", Count: 8}}
	r.Threads[0].Blocks = []string{"starter"}
	parent := "starter"
	r.Threads[0].Selected = &engagecontent.SelectedThread{Messages: []engagecontent.SelectedMessage{
		{ID: "starter", ThreadID: target.ThreadID, Blocks: []string{"starter"}, BodyState: "observed"},
		{ID: "reply", ThreadID: target.ThreadID, ParentID: &parent, Blocks: []string{"replysearchneedle"}, BodyState: "observed"},
	}, Replies: engagecontent.ReplyPage{EdgesObserved: 1, EdgesKnown: true}}
	name, title, kind, link := "Synthetic Author", "Synthetic attachment", "OpaqueNativeType", "https://example.test/attachment"
	r.Threads[0].Selected.Messages[0].Author.Name = &name
	r.Threads[0].Selected.Messages[0].Attachments = []engagecontent.SelectedAttachment{{ID: "attachment", Title: &title, NativeType: &kind, URL: &link}}
	r.Threads[0].Selected.Messages[0].Links = []engagecontent.SelectedLink{{URL: "https://example.test/link"}}
	return r
}
func TestEngageSelectedCLIEndToEnd(t *testing.T) {
	e := cloudCLIEnv(t)
	p := &engageFixturePage{}
	closer := engageControlled(t, p)
	old := observeEngageThread
	t.Cleanup(func() { observeEngageThread = old })
	observeEngageThread = func(_ context.Context, _ engagecontent.Driver, target engagecontent.ThreadTarget) (engagecontent.Result, error) {
		return selectedCLIResult(target), nil
	}
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target := "https://engage.cloud.microsoft/main/threads/" + id
	if code, _, err := e.run("content", "fetch", "engage-thread", "https://example.test/"); code == 0 {
		t.Fatal(err)
	}
	if closer.n != 0 {
		t.Fatal("invalid target launched browser")
	}
	code, out, stderr := e.run("content", "fetch", "engage-thread", target)
	if code != 0 {
		t.Fatal(out, stderr)
	}
	doc := decode(t, out)
	if doc["scope"] != engageThreadScope || closer.n != 1 {
		t.Fatal("selected scope or terminal Close missing")
	}
	code, out, stderr = e.run("content", "search", "replysearchneedle")
	if code != 0 || !strings.Contains(out, "replysearchneedle") || !strings.Contains(out, `"kind":"engage_thread"`) {
		t.Fatal(out, stderr)
	}
	code, out, stderr = e.run("content", "show", "engage-observation:1")
	if code != 0 || !strings.Contains(out, "replysearchneedle") || !strings.Contains(out, "selected") {
		t.Fatal(out, stderr)
	}
	code, out, stderr = e.run("--format", "text", "content", "show", "engage-observation:1")
	if code != 0 || !strings.Contains(out, "Synthetic Author") || !strings.Contains(out, "Synthetic attachment") || !strings.Contains(out, "https://example.test/link") {
		t.Fatal("text detail loses selected metadata", out, stderr)
	}
	if _, ok := manifest().Commands["content fetch engage-thread"]; !ok {
		t.Fatal("selected command missing from manifest")
	}
	closer.err = errors.New("synthetic close failed")
	if code, _, _ := e.run("content", "fetch", "engage-thread", target); code == 0 {
		t.Fatal("Close failure admitted")
	}
}
