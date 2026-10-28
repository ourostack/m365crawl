package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/errs"
)

func TestNavigationReplacementOnlyAfterNewDocumentCommit(t *testing.T) {
	for _, mode := range []string{"before-commit", "frame-before-commit", "waiting-ready", "during-ready", "frame-during-ready", "same-document-replaced"} {
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
					if mode == "frame-before-commit" || (evaluated.Load() && mode == "frame-during-ready") {
						id = "F2"
					}
					if mode == "before-commit" || mode == "same-document-replaced" || evaluated.Load() {
						loader = "other"
					}
				}
				return navigationFrameResult(id, loader, ""), nil
			})
			s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
				evaluated.Store(true)
				state := "complete"
				if mode == "waiting-ready" {
					state = "interactive"
				}
				return map[string]any{"result": map[string]any{"value": state}}, nil
			})
			err := p.Navigate(context.Background(), "https://example.test/requested")
			var replacement *NavigationReplacement
			want := mode == "waiting-ready" || mode == "during-ready"
			if err == nil || errors.As(err, &replacement) != want {
				t.Fatal("replacement capability escaped its committed same-frame boundary", err)
			}
		})
	}
}

func TestReplacementDocumentEvaluationPreservesBinding(t *testing.T) {
	for _, mode := range []string{"stable", "loader-change", "before-root", "after-root", "before-query", "after-query", "malformed", "destroyed", "missing-context", "hard-protocol", "script-error", "cancel-eval"} {
		t.Run(mode, func(t *testing.T) {
			s := browsertest.NewServer(t)
			p := testPage(t, s)
			var queries atomic.Int32
			s.Handle("Page.getFrameTree", func(browsertest.Request) (any, *browsertest.Error) {
				n := queries.Add(1)
				if (mode == "before-query" && n == 1) || (mode == "after-query" && n == 2) {
					return nil, &browsertest.Error{Code: -32000, Message: "synthetic query failed"}
				}
				if mode == "malformed" {
					return map[string]any{}, nil
				}
				id, loader := "F1", "L2"
				if (mode == "before-root" && n == 1) || (mode == "after-root" && n == 2) {
					id = "F2"
				}
				if mode == "loader-change" && n == 2 {
					loader = "L3"
				}
				return navigationFrameResult(id, loader, ""), nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.Handle("Runtime.evaluate", func(browsertest.Request) (any, *browsertest.Error) {
				switch mode {
				case "destroyed":
					return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed"}
				case "missing-context":
					return nil, &browsertest.Error{Code: -32000, Message: "Cannot find context with specified id"}
				case "hard-protocol":
					return nil, &browsertest.Error{Code: -32000, Message: "synthetic unrelated error"}
				case "script-error":
					return map[string]any{"exceptionDetails": map[string]any{}}, nil
				case "cancel-eval":
					cancel()
					return nil, &browsertest.Error{Code: -32000, Message: "Execution context was destroyed"}
				}
				return map[string]any{"result": map[string]any{"value": "complete"}}, nil
			})
			e := &NavigationReplacement{page: p, frameID: "F1"}
			var value string
			err := e.EvalReplacementDocument(ctx, "document.readyState", &value)
			if (err == nil) != (mode == "stable") {
				t.Fatal("unstable or failed evaluation became usable", err)
			}
			transition := mode == "loader-change" || mode == "destroyed" || mode == "missing-context"
			if errors.Is(err, errs.ErrBrowserDocumentTransition) != transition {
				t.Fatal("unrelated failure classified as transient document change", err)
			}
			if mode == "cancel-eval" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled evaluation became a retry", err)
			}
		})
	}
}

func TestReplacementCannotBeMintedBySharedErrorText(t *testing.T) {
	var e *NavigationReplacement
	if errors.As(errors.New("browser navigation was superseded"), &e) {
		t.Fatal("shared text granted replacement authority")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Page{}
	if err := p.navigationSuperseded(ctx, "F1", true); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled navigation granted a capability")
	}
	if err := e.EvalReplacementDocument(ctx, "document.readyState", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled evaluation ignored cancellation")
	}
	if err := e.EvalReplacementDocument(context.Background(), "", nil); err == nil {
		t.Fatal("nil error gained a document")
	}
	if err := (&NavigationReplacement{}).EvalReplacementDocument(context.Background(), "", nil); err == nil {
		t.Fatal("empty error gained a document")
	}
}
