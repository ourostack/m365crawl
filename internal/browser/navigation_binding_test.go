package browser

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

func TestNavigateWaitsForRequestedDocument(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	var acknowledged, committed, ready, requestedReadyRead atomic.Bool
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		acknowledged.Store(true)
		return map[string]any{"frameId": "F1", "loaderId": "requested"}, nil
	})
	s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
		loader := "previous"
		if committed.Load() {
			loader = "requested"
		}
		if acknowledged.Load() {
			committed.Store(true)
		}
		return map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "F1", "loaderId": loader}}}, nil
	})
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		state := "complete"
		if committed.Load() {
			if !ready.Swap(true) {
				state = "loading"
			} else {
				requestedReadyRead.Store(true)
			}
		}
		return map[string]any{"result": map[string]any{"value": state}}, nil
	})
	if err := p.Navigate(context.Background(), "https://example.test/requested"); err != nil {
		t.Fatal(err)
	}
	if !requestedReadyRead.Load() {
		t.Fatal("Navigate returned success using the complete previous document before the requested loader was ready")
	}
}
