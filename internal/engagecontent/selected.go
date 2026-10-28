package engagecontent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

//go:embed selected.js
var selectedJS string

const selectedStateExpr = `({Top:window===top,Allowed:location.protocol==="https:"&&location.hostname==="engage.cloud.microsoft"&&(!location.port||location.port==="443"),URL:location.href,Ready:document.readyState,Generation:window.__m365crawlEngageCapture?.Generation||""})`

func selectedScript(target ThreadTarget) string {
	encoded, _ := json.Marshal(target)
	return "(" + captureJS + ")((" + selectedJS + ")(" + projectionJS + "," + string(encoded) + "));"
}

// ObserveThread has the same exclusive-lease and terminal Close contract as Home.
func ObserveThread(ctx context.Context, page Driver, target ThreadTarget) (result Result, err error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	value := reflect.ValueOf(page)
	if page == nil || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return Result{}, &ReadError{Code: "invalid_driver"}
	}
	parsed, e := ParseThreadURL(target.URL)
	if e != nil || parsed.ThreadID != target.ThreadID {
		return Result{}, &ReadError{Code: "invalid_target"}
	}
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	defer func() {
		if e := work.Err(); e != nil {
			err = errors.Join(err, e)
		}
		if err != nil {
			result = Result{}
		}
	}()
	id, e := page.AddInitScript(work, selectedScript(target))
	if e != nil || id == "" {
		return Result{}, &ReadError{Code: "registration_failed"}
	}
	stopped := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stopBad := false
		if !stopped {
			stopBad = page.Eval(cleanup, stopReadExpr, nil) != nil
		}
		if page.RemoveInitScript(cleanup, id) != nil || stopBad {
			err = errors.Join(err, &ReadError{Code: "cleanup_failed"})
		}
	}()
	landing, closeLanding := context.WithTimeout(work, 30*time.Second)
	defer closeLanding()
	generation, e := selectedLanding(landing, page, target)
	if e != nil {
		return Result{}, e
	}
	closeLanding()
	if err := waitCollection(work); err != nil {
		return Result{}, err
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
	return decodeSelected(work, target, raw)
}
