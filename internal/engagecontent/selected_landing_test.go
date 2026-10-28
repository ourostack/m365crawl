package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

type landingDriver struct {
	selectedDriver
	navErr           error
	states           []map[string]any
	evalErr          error
	cancel           context.CancelFunc
	cancelNavigation context.CancelFunc
}

func (d *landingDriver) Navigate(ctx context.Context, target string) error {
	d.events = append(d.events, "navigate:"+target)
	if d.cancelNavigation != nil {
		d.cancelNavigation()
	}
	return d.navErr
}

func (d *landingDriver) Eval(ctx context.Context, expr string, out any) error {
	if expr != selectedStateExpr {
		return d.selectedDriver.Eval(ctx, expr, out)
	}
	d.events = append(d.events, "state")
	if d.cancel != nil {
		d.cancel()
	}
	if d.evalErr != nil {
		e := d.evalErr
		if errors.Is(e, errs.ErrBrowserDocumentTransition) {
			d.evalErr = nil
		}
		return e
	}
	if len(d.states) == 0 {
		return d.selectedDriver.Eval(ctx, expr, out)
	}
	state := d.states[0]
	if len(d.states) > 1 {
		d.states = d.states[1:]
	}
	b, _ := json.Marshal(state)
	return json.Unmarshal(b, out)
}

// This explicit test capability models only the browser's opaque error boundary.
// The real browser/observer causal test separately proves who can produce it.
type landingReplacement struct{ page *landingDriver }

func (*landingReplacement) Error() string { return "browser navigation was superseded" }
func (e *landingReplacement) EvalReplacementDocument(ctx context.Context, expr string, out any) error {
	return e.page.Eval(ctx, expr, out)
}

func landingState(target ThreadTarget, generation string) map[string]any {
	return map[string]any{"Top": true, "Allowed": true, "URL": target.URL, "Ready": "complete", "Generation": generation}
}

func TestSelectedLandingConditionsAndHardErrors(t *testing.T) {
	quickCollection(t)
	target, raw := selectedFixture(t)
	for _, mode := range []string{"normal", "replacement", "transient", "intermediate", "loading", "shared-text", "hard-eval", "wrong-host", "child", "unknown-route", "wrong-target", "missing-generation", "oversize-generation", "cancel", "cancel-navigation", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			d := &landingDriver{selectedDriver: selectedDriver{fakeDriver: fakeDriver{raw: raw}}}
			state := landingState(target, raw.Generation)
			d.states = []map[string]any{state}
			success := mode == "normal" || mode == "replacement" || mode == "transient" || mode == "intermediate" || mode == "loading"
			if mode != "normal" {
				d.navErr = &landingReplacement{d}
			}
			switch mode {
			case "transient":
				d.evalErr = errs.ErrBrowserDocumentTransition
			case "intermediate", "loading":
				first := landingState(target, "intermediate-generation")
				if mode == "intermediate" {
					first["URL"] = "https://engage.cloud.microsoft/main/intermediate"
				} else {
					first["Ready"] = "loading"
				}
				d.states = []map[string]any{first, state}
			case "shared-text":
				d.navErr = errors.New("browser navigation was superseded")
			case "hard-eval":
				d.evalErr = errors.New("synthetic unrelated evaluation failure")
			case "wrong-host":
				state["Allowed"] = false
				state["URL"] = "https://example.test/"
			case "child":
				state["Top"] = false
			case "unknown-route":
				state["URL"] = "https://engage.cloud.microsoft/main/intermediate"
			case "wrong-target":
				state["URL"] = strings.Replace(target.URL, selectedTestID(), "eyJfdHlwZSI6IlRocmVhZCIsImlkIjoiNDMifQ", 1)
			case "missing-generation":
				state["Generation"] = ""
			case "oversize-generation":
				state["Generation"] = strings.Repeat("x", 129)
			case "cancel":
				d.cancel = cancel
			case "cancel-navigation":
				d.cancelNavigation = cancel
			case "deadline":
				state["Ready"] = "loading"
			}
			if !success && (mode == "unknown-route" || mode == "wrong-target" || mode == "missing-generation" || mode == "oversize-generation" || mode == "deadline") {
				var shortCancel context.CancelFunc
				ctx, shortCancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer shortCancel()
			}
			got, err := ObserveThread(ctx, d, target)
			if (err == nil) != success {
				t.Fatal("incorrect landing eligibility", err)
			}
			if !success && !reflect.DeepEqual(got, Result{}) {
				t.Fatal("rejected landing leaked a result")
			}
			if success && got.Threads[0].Selected.Messages[1].Blocks[0] != "reply-only-search" {
				t.Fatal("fresh selected reply lost")
			}
			if (mode == "cancel" || mode == "cancel-navigation") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation became an eligible document", err)
			}
			if mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("landing deadline disappeared", err)
			}
			navigations, registrations := 0, 0
			for _, event := range d.events {
				if strings.HasPrefix(event, "navigate:") {
					navigations++
				}
				if event == "register" {
					registrations++
				}
			}
			if navigations != 1 || registrations != 1 {
				t.Fatal("landing rearmed or renavigated")
			}
		})
	}
}

func TestSelectedReplacementStillRequiresFinalAdmission(t *testing.T) {
	quickCollection(t)
	for _, mode := range []string{"viewer", "network", "target", "generation", "budget", "fatal", "stop", "remove"} {
		t.Run(mode, func(t *testing.T) {
			target, raw := selectedFixture(t)
			d := &landingDriver{selectedDriver: selectedDriver{fakeDriver: fakeDriver{raw: raw}}}
			d.navErr = &landingReplacement{d}
			d.states = []map[string]any{landingState(target, raw.Generation)}
			switch mode {
			case "viewer":
				d.raw.AccountEvidence = nil
			case "network":
				d.raw.Account.NetworkID = ""
			case "target":
				d.raw.Threads[0].ID = "another"
			case "generation":
				d.raw.Generation = "later-document"
			case "budget":
				d.raw.Fatal = "too_large"
			case "fatal":
				d.raw.Fatal = "capture_incomplete"
			case "stop":
				d.fail = "stop-read"
			case "remove":
				d.fail = "remove"
			}
			got, err := ObserveThread(context.Background(), d, target)
			if err == nil || !reflect.DeepEqual(got, Result{}) {
				t.Fatal("replacement waived final admission or cleanup", err)
			}
		})
	}
}

func TestSelectedLandingSnapshotRunsOnActualPageState(t *testing.T) {
	target, _ := selectedFixture(t)
	for _, mode := range []string{"ready", "child", "wrong-host", "wrong-port", "http", "loading", "no-capture"} {
		t.Run(mode, func(t *testing.T) {
			script := `global.window=global;global.top=global;global.location=new URL(` + string(rawValue(target.URL)) + `);
				global.document={readyState:"complete"};window.__m365crawlEngageCapture={Generation:"fresh"};`
			switch mode {
			case "child":
				script += `global.top={};`
			case "wrong-host":
				script += `location=new URL("https://example.test/");`
			case "wrong-port":
				script += `location.port="8443";`
			case "http":
				script += `location.protocol="http:";`
			case "loading":
				script += `document.readyState="loading";`
			case "no-capture":
				script += `delete window.__m365crawlEngageCapture;`
			}
			script += `console.log(JSON.stringify(` + selectedStateExpr + `));`
			cmd := exec.CommandContext(context.Background(), "node", "-")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err, string(out))
			}
			var state struct {
				Top, Allowed           bool
				URL, Ready, Generation string
			}
			if json.Unmarshal(out, &state) != nil {
				t.Fatal("invalid snapshot")
			}
			if state.Top != (mode != "child") || state.Allowed != (mode != "wrong-host" && mode != "wrong-port" && mode != "http") {
				t.Fatal("snapshot did not test the actual top/host")
			}
			if mode == "ready" && (state.URL != target.URL || state.Ready != "complete" || state.Generation != "fresh") {
				t.Fatal("eligible document state missing")
			}
			if mode == "loading" && state.Ready != "loading" {
				t.Fatal("loading became ready")
			}
			if mode == "no-capture" && state.Generation != "" {
				t.Fatal("generation invented")
			}
		})
	}
}
