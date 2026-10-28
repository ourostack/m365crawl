package browser_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/engagecontent"
)

// Removing the selected handoff must reproduce the real navigation_failed boundary,
// not silently accept the generic navigator's superseded document.
func TestSelectedLandingAfterCommittedReplacement(t *testing.T) {
	s := browsertest.NewServer(t)
	p := browser.SelectedTestPage(t, s)
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target, err := engagecontent.ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + id)
	if err != nil {
		t.Fatal(err)
	}
	var ack, requestedRead atomic.Bool
	var frameReads atomic.Int32
	s.Handle("Page.enable", func(browsertest.Request) (any, *browsertest.Error) { return map[string]any{}, nil })
	s.Handle("Page.addScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"identifier": "owned"}, nil
	})
	s.Handle("Page.removeScriptToEvaluateOnNewDocument", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{}, nil
	})
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		ack.Store(true)
		return map[string]any{"frameId": "F", "loaderId": "L1"}, nil
	})
	s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
		loader := "L0"
		if ack.Load() {
			loader = "L1"
			if requestedRead.Load() {
				loader = "L2"
				if frameReads.Add(1) > 1 {
					loader = "L3"
				}
			}
		}
		return map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "F", "loaderId": loader}}}, nil
	})
	s.Handle("Runtime.evaluate", func(req browsertest.Request) (any, *browsertest.Error) {
		var params struct{ Expression string }
		if json.Unmarshal(req.Params, &params) != nil {
			t.Error("malformed evaluation")
		}
		var value any
		switch {
		case params.Expression == "document.readyState":
			requestedRead.Store(true)
			value = "interactive"
		case params.Expression == "location.host":
			value = "engage.cloud.microsoft"
		case strings.Contains(params.Expression, ".stop()"):
			value = map[string]any{
				"State": "observations", "Generation": "final-generation",
				"Account": map[string]any{"Host": "engage.cloud.microsoft", "UserID": "viewer", "NetworkID": "network"},
				"AccountEvidence": []any{map[string]any{"Host": "engage.cloud.microsoft", "UserID": "viewer", "NetworkID": "network"}},
				"Threads": []any{map[string]any{
					"ID": id, "NetworkID": "network", "StarterID": "starter", "Blocks": []string{"useful starter"},
					"Selected": map[string]any{"messages": []any{
						map[string]any{"id": "starter", "thread_id": id, "blocks": []string{"useful starter"}, "body_state": "observed"},
						map[string]any{"id": "reply", "thread_id": id, "parent_id": "starter", "blocks": []string{"useful reply"}, "body_state": "observed"},
					}, "replies": map[string]any{"edges_observed": 1}},
				}},
			}
		default:
			value = map[string]any{"Top": true, "Allowed": true, "URL": target.URL, "Ready": "complete", "Generation": "final-generation"}
		}
		return map[string]any{"result": map[string]any{"value": value}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, err := engagecontent.ObserveThread(ctx, p, target)
	if err != nil {
		t.Fatalf("committed same-frame replacement never reached fresh eligible landing: %v", err)
	}
	if got.Selection == nil || len(got.Threads) != 1 || got.Threads[0].Selected.Messages[1].Blocks[0] != "useful reply" {
		t.Fatal("fresh selected evidence was not preserved")
	}
	counts := map[string]int{}
	for _, req := range s.Requests() {
		counts[req.Method]++
	}
	if counts["Page.navigate"] != 1 || counts["Page.addScriptToEvaluateOnNewDocument"] != 1 || counts["Page.removeScriptToEvaluateOnNewDocument"] != 1 {
		t.Fatal("handoff navigated or registered more than once, or did not remove the observer", counts)
	}
}
