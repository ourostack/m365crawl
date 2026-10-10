package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeDriver struct {
	events          []string
	fail            string
	host            string
	raw             scriptResult
	noHome          bool
	wrongPage       bool
	cleanupBad      bool
	noClick         bool
	finalHostBad    bool
	cancelAtEval    context.CancelFunc
	clickGeneration string
}

func (f *fakeDriver) AddInitScript(ctx context.Context, source string) (string, error) {
	f.events = append(f.events, "register")
	if source != initScript || f.fail == "register" {
		return "", errors.New("synthetic private registration error")
	}
	return "owned-registration", ctx.Err()
}

func (f *fakeDriver) RemoveInitScript(_ context.Context, id string) error {
	f.events = append(f.events, "remove:"+id)
	if f.fail == "remove" || f.cleanupBad {
		return errors.New("synthetic private removal error")
	}
	return nil
}

func (f *fakeDriver) Navigate(_ context.Context, target string) error {
	f.events = append(f.events, "navigate:"+target)
	if f.fail == "navigate" {
		return errors.New("synthetic private navigation error")
	}
	return nil
}

func (f *fakeDriver) Host(context.Context) (string, error) {
	final := len(f.events) > 0 && f.events[len(f.events)-1] == "home-click"
	f.events = append(f.events, "host")
	if final && f.finalHostBad {
		return "login.microsoftonline.com", nil
	}
	if f.fail == "host" {
		return "", errors.New("synthetic host refusal")
	}
	if f.host != "" {
		return f.host, nil
	}
	return "engage.cloud.microsoft", nil
}

func (f *fakeDriver) Eval(_ context.Context, expr string, out any) error {
	var value any
	switch expr {
	case homeStateExpr:
		f.events = append(f.events, "home-state")
		value = map[string]any{"Allowed": !f.wrongPage, "Home": !f.noHome, "Generation": f.raw.Generation}
	case homeClickExpr:
		f.events = append(f.events, "home-click")
		generation := f.raw.Generation
		if f.clickGeneration != "" {
			generation = f.clickGeneration
		}
		value = map[string]any{"Clicked": !f.noHome && !f.noClick, "Generation": generation}
	case stopReadExpr:
		f.events = append(f.events, "stop-read")
		value = f.raw
	default:
		return errors.New("unexpected synthetic eval expression")
	}
	if f.fail == f.events[len(f.events)-1] {
		if f.cancelAtEval != nil {
			f.cancelAtEval()
		}
		return errors.New("synthetic private evaluation error")
	}

	if out == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func quickCollection(t *testing.T) {
	t.Helper()
	previous := waitCollection
	waitCollection = func(ctx context.Context) error { return ctx.Err() }
	t.Cleanup(func() { waitCollection = previous })
}

func TestObserveFinalHostAndNativeClickRace(t *testing.T) {
	quickCollection(t)
	for _, tc := range []struct {
		noClick bool
		code    string
	}{
		{true, "home_unavailable"}, {false, "unavailable"},
	} {
		f := &fakeDriver{raw: nativeResult(), noClick: tc.noClick, finalHostBad: !tc.noClick}
		got, err := ObserveHome(context.Background(), f)
		var coded *ReadError
		if !errors.As(err, &coded) || coded.Code != tc.code || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("native state changed: %+v, %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeDriver{fail: "home-state", cancelAtEval: cancel}
	if got, err := ObserveHome(ctx, f); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("evaluation cancellation disappeared: %+v, %v", got, err)
	}
}

func TestCollectionWaitUsesContextAndTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitCollection(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("collection ignores cancellation: %v", err)
	}
	if err := wait(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestObserveHomeLifecycle(t *testing.T) {
	quickCollection(t)
	f := &fakeDriver{raw: nativeResult()}
	got, err := ObserveHome(context.Background(), f)
	if err != nil || got.Qualification != "native_home_observations" || got.Complete || len(got.Threads) != 1 ||
		!reflect.DeepEqual(got.Threads[0].Blocks, []string{"initial", "", "later\n"}) {
		t.Fatalf("native-session publication: %+v, %v", got, err)
	}
	if !reflect.DeepEqual(f.events, []string{"register", "navigate:https://engage.cloud.microsoft/", "host", "home-state", "home-click", "host", "stop-read", "remove:owned-registration"}) {
		t.Fatalf("owned lifecycle: %v", f.events)
	}
}

func TestObserveHomeTerminalFailures(t *testing.T) {
	quickCollection(t)
	for _, tc := range []struct{ phase, code string }{
		{"register", "registration_failed"},
		{"navigate", "navigation_failed"},
		{"host", "unavailable"},
		{"home-state", "capture_failed"},
		{"home-click", "capture_failed"},
		{"stop-read", "capture_failed"},
		{"remove", "cleanup_failed"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			f := &fakeDriver{fail: tc.phase, raw: nativeResult()}
			got, err := ObserveHome(context.Background(), f)
			var coded *ReadError
			if !errors.As(err, &coded) || coded.Code != tc.code || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("terminal refusal: %+v, %v", got, err)
			}
			if tc.phase == "register" {
				if len(f.events) != 1 {
					t.Fatalf("unknown registration retried/removed: %v", f.events)
				}
			} else if f.events[len(f.events)-1] != "remove:owned-registration" {
				t.Fatalf("known registration not cleaned: %v", f.events)
			}
		})
	}
	for _, f := range []*fakeDriver{
		{host: "login.microsoftonline.com", raw: nativeResult()},
		{host: "engage.cloud.microsoft:8443", raw: nativeResult()},
		{wrongPage: true, raw: nativeResult()},
	} {
		got, err := ObserveHome(context.Background(), f)
		var coded *ReadError
		if !errors.As(err, &coded) || coded.Code != "unavailable" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unqualified native page: %+v, %v", got, err)
		}
	}
}

func TestObserveHomeCancellationAndSecondaryCleanup(t *testing.T) {
	quickCollection(t)
	if got, err := ObserveHome(context.Background(), nil); err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("nil driver: %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ObserveHome(ctx, &fakeDriver{}); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled capture: %+v, %v", got, err)
	}
	f := &fakeDriver{fail: "navigate", cleanupBad: true}
	_, err := ObserveHome(context.Background(), f)
	if err == nil || err.Error() != "engage_content: navigation_failed\nengage_content: cleanup_failed" {
		t.Fatalf("secondary failure missing/private error leaked: %v", err)
	}
	previous := waitCollection
	waitCollection = func(context.Context) error { return context.Canceled }
	f = &fakeDriver{raw: nativeResult()}
	if got, err := ObserveHome(context.Background(), f); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("collection cancelled: %+v, %v", got, err)
	}
	waitCollection = previous
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	f = &fakeDriver{noHome: true}
	if got, err := ObserveHome(ctx, f); !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("missing native Home cancellation: %+v, %v", got, err)
	}
}
