package browser

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func navigationFrameResult(id, loader, parent string) any {
	return map[string]any{"frameTree": map[string]any{"frame": map[string]any{
		"id": id, "loaderId": loader, "parentId": parent,
	}}}
}

func TestNavigateRefusesMalformedBinding(t *testing.T) {
	before := navigationFrameResult("F1", "previous", "")
	after := navigationFrameResult("F1", "requested", "")
	response := map[string]any{"frameId": "F1", "loaderId": "requested"}
	for _, tc := range []struct {
		name                    string
		before, response, after any
	}{
		{"missing-initial-frame", map[string]any{}, response, after},
		{"missing-initial-loader", navigationFrameResult("F1", "", ""), response, after},
		{"child-initial-frame", navigationFrameResult("F1", "previous", "parent"), response, after},
		{"missing-response-frame", before, map[string]any{}, after},
		{"wrong-response-frame", before, map[string]any{"frameId": "F2", "loaderId": "requested"}, after},
		{"null-response-loader", before, map[string]any{"frameId": "F1", "loaderId": nil}, before},
		{"empty-response-loader", before, map[string]any{"frameId": "F1", "loaderId": ""}, before},
		{"typed-response-loader", before, map[string]any{"frameId": "F1", "loaderId": 7}, after},
		{"download", before, map[string]any{"frameId": "F1", "loaderId": "requested", "isDownload": true}, after},
		{"missing-committed-frame", before, response, map[string]any{}},
		{"missing-committed-loader", before, response, navigationFrameResult("F1", "", "")},
		{"child-committed-frame", before, response, navigationFrameResult("F1", "requested", "parent")},
		{"malformed-frame-tree", before, response, map[string]any{"frameTree": "not-an-object"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := browsertest.NewServer(t)
			p := testPage(t, s)
			var ack atomic.Bool
			s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
				if ack.Load() {
					return tc.after, nil
				}
				return tc.before, nil
			})
			s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
				ack.Store(true)
				return tc.response, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := p.Navigate(ctx, "https://example.test/requested"); err == nil {
				t.Fatal("invalid document identity or download became successful navigation")
			}
		})
	}
}

func TestNavigateSameDocumentKeepsBoundLoader(t *testing.T) {
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
		return navigationFrameResult("F1", "committed", ""), nil
	})
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"frameId": "F1"}, nil
	})
	if err := p.Navigate(context.Background(), "https://example.test/page#section"); err != nil {
		t.Fatal("documented same-document acknowledgment should preserve the bound committed loader", err)
	}
}

func TestNavigateRejectsSupersededDocument(t *testing.T) {
	for _, mode := range []string{"before-commit", "frame-before-commit", "waiting-ready", "during-ready", "frame-during-ready", "previous-loader-returned", "same-document-replaced"} {
		t.Run(mode, func(t *testing.T) {
			s := browsertest.NewServer(t)
			p := testPage(t, s)
			var ack, evaluated atomic.Bool
			s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
				ack.Store(true)
				if mode == "same-document-replaced" {
					return map[string]any{"frameId": "F1"}, nil
				}
				return map[string]any{"frameId": "F1", "loaderId": "requested"}, nil
			})
			s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
				id, loader := "F1", "previous"
				if ack.Load() {
					loader = "requested"
					if mode == "frame-before-commit" {
						id = "F2"
					}
					if mode == "before-commit" || mode == "same-document-replaced" || evaluated.Load() {
						loader = "other"
					}
					if evaluated.Load() && mode == "frame-during-ready" {
						id = "F2"
					}
					if evaluated.Load() && mode == "previous-loader-returned" {
						loader = "previous"
					}
				}
				return navigationFrameResult(id, loader, ""), nil
			})
			s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
				evaluated.Store(true)
				state := "complete"
				if mode == "waiting-ready" || mode == "previous-loader-returned" {
					state = "loading"
				}
				return map[string]any{"result": map[string]any{"value": state}}, nil
			})
			if err := p.Navigate(context.Background(), "https://example.test/requested"); err == nil || !strings.Contains(err.Error(), "superseded") {
				t.Fatal("a different committed document was accepted", err)
			}
		})
	}
}

func TestNavigateBindingQueryErrorsAndCancellation(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			s := browsertest.NewServer(t)
			p := testPage(t, s)
			var calls atomic.Int32
			s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
				n := calls.Add(1)
				if int(n) == failAt {
					return nil, &browsertest.Error{Code: -32000, Message: "synthetic binding query failure"}
				}
				loader := "previous"
				if n > 1 {
					loader = "requested"
				}
				return navigationFrameResult("F1", loader, ""), nil
			})
			s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
				return map[string]any{"frameId": "F1", "loaderId": "requested"}, nil
			})
			var protocol *CDPError
			if err := p.Navigate(context.Background(), "https://example.test/requested"); !errors.As(err, &protocol) || protocol.Code != -32000 {
				t.Fatal("binding query failure was hidden", err)
			}
		})
	}
	s := browsertest.NewServer(t)
	p := testPage(t, s)
	s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
		return navigationFrameResult("F1", "previous", ""), nil
	})
	s.Handle("Page.navigate", func(browsertest.Request) (any, *browsertest.Error) {
		return map[string]any{"frameId": "F1", "loaderId": "requested"}, nil
	})
	var evaluations atomic.Int32
	s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
		evaluations.Add(1)
		return map[string]any{"result": map[string]any{"value": "complete"}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Navigate(ctx, "https://example.test/requested"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pending commit must obey caller deadline", err)
	}
	if evaluations.Load() != 0 {
		t.Fatal("old document was evaluated as requested-document readiness")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := p.Navigate(cancelled, "https://example.test/requested"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled caller lost its error", err)
	}
}
