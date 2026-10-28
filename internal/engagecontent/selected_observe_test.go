package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type selectedDriver struct {
	fakeDriver
}

func (d *selectedDriver) AddInitScript(ctx context.Context, source string) (string, error) {
	d.events = append(d.events, "register")
	if !strings.Contains(source, "threadStarter") || d.fail == "register" {
		return "", errors.New("synthetic registration failed")
	}
	return "selected-registration", ctx.Err()
}
func (d *selectedDriver) Eval(ctx context.Context, expr string, out any) error {
	if expr == selectedStateExpr {
		d.events = append(d.events, "state")
		if d.fail == "state" {
			return errors.New("synthetic state failed")
		}
		if out == nil {
			return nil
		}
		generation := d.raw.Generation
		if d.fail == "generation" {
			generation = "other-generation"
		}
		b, _ := json.Marshal(map[string]any{"Allowed": !d.wrongPage, "Generation": generation})
		return json.Unmarshal(b, out)
	}
	return d.fakeDriver.Eval(ctx, expr, out)
}
func TestObserveSelectedThreadLifecycle(t *testing.T) {
	quickCollection(t)
	target, raw := selectedFixture(t)
	for _, failure := range []string{"", "register", "navigate", "host", "state", "stop-read", "remove", "generation"} {
		d := &selectedDriver{fakeDriver: fakeDriver{raw: raw, fail: failure}}
		got, err := ObserveThread(context.Background(), d, target)
		if (err == nil) != (failure == "") {
			t.Fatal("incorrect lifecycle outcome", failure, err)
		}
		if err == nil && got.Selection == nil {
			t.Fatal("selection missing")
		}
	}
	d := &selectedDriver{fakeDriver: fakeDriver{raw: raw, wrongPage: true}}
	if _, err := ObserveThread(context.Background(), d, target); err == nil {
		t.Fatal("foreign document admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveThread(ctx, d, target); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if _, err := ObserveThread(context.Background(), nil, target); err == nil {
		t.Fatal("nil driver admitted")
	}
	var typedNil *selectedDriver
	if _, err := ObserveThread(context.Background(), typedNil, target); err == nil {
		t.Fatal("typed nil driver admitted")
	}
	badTarget := target
	badTarget.URL += "?sig=private"
	if _, err := ObserveThread(context.Background(), d, badTarget); err == nil {
		t.Fatal("invalid backend target admitted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	oldWait := waitCollection
	waitCollection = func(ctx context.Context) error { cancel(); return ctx.Err() }
	if _, err := ObserveThread(ctx, &selectedDriver{fakeDriver: fakeDriver{raw: raw}}, target); !errors.Is(err, context.Canceled) {
		t.Fatal("work cancellation lost")
	}
	waitCollection = oldWait
	foreign := &selectedDriver{fakeDriver: fakeDriver{raw: raw, host: "login.microsoftonline.com"}}
	if _, err := ObserveThread(context.Background(), foreign, target); err == nil {
		t.Fatal("foreign final host admitted")
	}
}
