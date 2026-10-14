package engagecontent

import (
	"context"
	"reflect"
)

// HomeReady is preliminary UI readiness, not positive authentication evidence.
// It never navigates, clicks, registers an observer, or requires a generation.
func HomeReady(ctx context.Context, page Driver) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if page == nil || (reflect.ValueOf(page).Kind() == reflect.Pointer && reflect.ValueOf(page).IsNil()) {
		return false, &ReadError{Code: "invalid_driver"}
	}
	host, err := page.Host(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, &ReadError{Code: "unavailable"}
	}
	if host != "engage.cloud.microsoft" && host != "engage.cloud.microsoft:443" {
		return false, nil
	}
	var state struct{ Allowed, Home bool }
	if err := page.Eval(ctx, homeStateExpr, &state); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, &ReadError{Code: "capture_failed"}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return state.Allowed && state.Home, nil
}
