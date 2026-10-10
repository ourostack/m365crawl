package engagecontent

import (
	"context"
	"errors"
	"time"
)

type Driver interface {
	Navigate(context.Context, string) error
	Host(context.Context) (string, error)
	Eval(context.Context, string, any) error
	AddInitScript(context.Context, string) (string, error)
	RemoveInitScript(context.Context, string) error
}

const homeControlExpr = `(() => {
	const label = node => (node.getAttribute("aria-label") || node.textContent || "").trim();
	const visible = node => {
		if(node.disabled || node.getAttribute("aria-disabled")==="true" || node.closest("[inert]")) return false;
		for(let current=node;current instanceof Element;current=current.parentElement) {
			const style=getComputedStyle(current);
			if(style.display==="none" || style.visibility==="hidden" || style.visibility==="collapse" ||
			   Number(style.opacity)===0 || style.contentVisibility==="hidden" || style.pointerEvents==="none") return false;
		}
		return [...node.getClientRects()].some(rect=>rect.width>0 && rect.height>0 && rect.right>0 && rect.bottom>0 && rect.left<innerWidth && rect.top<innerHeight);
	};
	return [...document.querySelectorAll('button,[role="tab"],a')].find(node=>label(node)==="Home" && visible(node)) || null;
})()`

const homeStateExpr = `(() => {
	return {
		Allowed:location.protocol==="https:" && location.hostname==="engage.cloud.microsoft" && (!location.port || location.port==="443"),
		Generation:window.__m365crawlEngageCapture?.Generation || "",
		Home:!!` + homeControlExpr + `
	};
})()`

const homeClickExpr = `(() => {
	const Generation=window.__m365crawlEngageCapture?.Generation || "";
	if(location.protocol!=="https:" || location.hostname!=="engage.cloud.microsoft" || (location.port && location.port!=="443")) return {Clicked:false,Generation};
	const node=` + homeControlExpr + `;
	if(!node) return {Clicked:false,Generation};
	node.click();
	return {Clicked:true,Generation};
})()`

var waitCollection = func(ctx context.Context) error { return wait(ctx, 10*time.Second) }

// ObserveHome returns bounded native-session observations, not a complete feed.
// Its caller owns the exclusive browser lease and must Close it on every path.
func ObserveHome(ctx context.Context, page Driver) (result Result, err error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if page == nil {
		return Result{}, &ReadError{Code: "invalid_driver"}
	}
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	defer func() {
		if e := work.Err(); e != nil && !errors.Is(err, e) {
			err = errors.Join(err, e)
		}
		if err != nil {
			result = Result{}
		}
	}()
	id, e := page.AddInitScript(work, initScript)
	if e != nil || id == "" {
		return Result{}, &ReadError{Code: "registration_failed"}
	}
	stopped := false
	defer func() {
		cleanup, close := context.WithTimeout(context.Background(), 3*time.Second)
		defer close()
		failed := false
		if !stopped {
			failed = page.Eval(cleanup, stopReadExpr, nil) != nil
		}
		if page.RemoveInitScript(cleanup, id) != nil {
			failed = true
		}
		if failed {
			err = errors.Join(err, &ReadError{Code: "cleanup_failed"})
		}
	}()
	landing, closeLanding := context.WithTimeout(work, 30*time.Second)
	defer closeLanding()
	if page.Navigate(landing, "https://engage.cloud.microsoft/") != nil {
		return Result{}, &ReadError{Code: "navigation_failed"}
	}
	generation := ""
	for {
		host, e := page.Host(landing)
		if e != nil || (host != "engage.cloud.microsoft" && host != "engage.cloud.microsoft:443") {
			return Result{}, &ReadError{Code: "unavailable"}
		}
		var state struct {
			Allowed, Home bool
			Generation    string
		}
		if page.Eval(landing, homeStateExpr, &state) != nil {
			return Result{}, &ReadError{Code: "capture_failed"}
		}
		if !state.Allowed {
			return Result{}, &ReadError{Code: "unavailable"}
		}
		if state.Home {
			if state.Generation == "" || len(state.Generation) > 128 {
				return Result{}, &ReadError{Code: "capture_failed"}
			}
			generation = state.Generation
			break
		}
		if e := wait(landing, 250*time.Millisecond); e != nil {
			return Result{}, e
		}
	}
	var action struct {
		Clicked    bool
		Generation string
	}
	if page.Eval(landing, homeClickExpr, &action) != nil {
		return Result{}, &ReadError{Code: "capture_failed"}
	}
	if !action.Clicked {
		return Result{}, &ReadError{Code: "home_unavailable"}
	}
	if action.Generation != generation {
		return Result{}, &ReadError{Code: "document_changed"}
	}
	closeLanding()
	if e := waitCollection(work); e != nil {
		return Result{}, e
	}
	host, e := page.Host(work)
	if e != nil || (host != "engage.cloud.microsoft" && host != "engage.cloud.microsoft:443") {
		return Result{}, &ReadError{Code: "unavailable"}
	}
	var raw scriptResult
	if page.Eval(work, stopReadExpr, &raw) != nil {
		return Result{}, &ReadError{Code: "capture_failed"}
	}
	stopped = true
	if raw.Generation != generation {
		return Result{}, &ReadError{Code: "document_changed"}
	}
	return decode(work, raw)
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
