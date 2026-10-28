package engagecontent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Only the owned browser's opaque committed/same-frame navigation error supplies
// this capability. A generic error (including identical text) cannot grant it.
type replacementNavigation interface {
	error
	EvalReplacementDocument(context.Context, string, any) error
}

// selectedLanding attempts document eligibility, not native result admission.
// Viewer/network/target proof remains at final decode after collection.
func selectedLanding(ctx context.Context, page Driver, target ThreadTarget) (string, error) {
	eval := page.Eval
	err := page.Navigate(ctx, target.URL)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var replacement replacementNavigation
		if !errors.As(err, &replacement) {
			return "", &ReadError{Code: "navigation_failed"}
		}
		eval = replacement.EvalReplacementDocument
	}
	for {
		var state struct {
			Top, Allowed           bool
			URL, Ready, Generation string
		}
		err := eval(ctx, selectedStateExpr, &state)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil && !errors.Is(err, errs.ErrBrowserDocumentTransition) {
			return "", &ReadError{Code: "unavailable"}
		}
		if err == nil {
			if !state.Top || !state.Allowed {
				return "", &ReadError{Code: "unavailable"}
			}
			route, routeErr := ParseThreadURL(state.URL)
			if routeErr == nil && strings.HasPrefix(state.URL, "https://engage.cloud.microsoft") &&
				route.ThreadID == target.ThreadID && state.Ready == "complete" &&
				state.Generation != "" && len(state.Generation) <= 128 {
				return state.Generation, nil
			}
		}
		if err := wait(ctx, 250*time.Millisecond); err != nil {
			return "", err
		}
	}
}
