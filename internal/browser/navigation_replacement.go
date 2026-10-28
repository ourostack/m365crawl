package browser

import (
	"context"
	"errors"

	"github.com/ourostack/m365crawl/internal/errs"
)

// NavigationReplacement is still a failed Navigate. Only a positively committed
// new-document navigation replaced in the same root frame produces this type.
// Callers may separately qualify a fresh document; it conveys no route or access proof.
type NavigationReplacement struct {
	page    *Page
	frameID string
}

func (*NavigationReplacement) Error() string { return "browser navigation was superseded" }

func (p *Page) navigationSuperseded(ctx context.Context, frameID string, committedSameFrame bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if committedSameFrame {
		return &NavigationReplacement{page: p, frameID: frameID}
	}
	return errors.New("browser navigation was superseded")
}

// EvalReplacementDocument runs a readonly expression on the same owned root and brackets it with
// document bindings. A transition is not a successful evaluation or navigation.
func (e *NavigationReplacement) EvalReplacementDocument(ctx context.Context, expr string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || e.page == nil || e.frameID == "" {
		return errors.New("browser replacement binding missing")
	}
	before, err := e.page.navigationFrame(ctx)
	if err != nil {
		return err
	}
	if before.ID != e.frameID {
		return errors.New("browser replacement root changed")
	}
	if err := e.page.Eval(ctx, expr, out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isDestroyed(err) {
			return errs.ErrBrowserDocumentTransition
		}
		return err
	}
	after, err := e.page.navigationFrame(ctx)
	if err != nil {
		return err
	}
	if after.ID != e.frameID {
		return errors.Join(ctx.Err(), errors.New("browser replacement root changed"))
	}
	if after.LoaderID != before.LoaderID {
		return errors.Join(ctx.Err(), errs.ErrBrowserDocumentTransition)
	}
	return ctx.Err()
}
