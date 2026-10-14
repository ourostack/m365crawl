package engagecontent

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type cancellingReady struct {
	*fakeDriver
	cancel context.CancelFunc
	phase  string
}

func (d cancellingReady) Host(ctx context.Context) (string, error) {
	if d.phase == "host" {
		d.cancel()
		return "", errors.New("disconnect")
	}
	return d.fakeDriver.Host(ctx)
}
func (d cancellingReady) Eval(ctx context.Context, s string, out any) error {
	d.cancel()
	if d.phase == "eval" {
		return errors.New("disconnect")
	}
	return d.fakeDriver.Eval(ctx, s, out)
}

func TestEngageHomeReadyTerminalCancellation(t *testing.T) {
	for _, phase := range []string{"host", "eval", "after"} {
		ctx, cancel := context.WithCancel(context.Background())
		d := cancellingReady{&fakeDriver{}, cancel, phase}
		ok, err := HomeReady(ctx, d)
		cancel()
		if ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v %v", phase, ok, err)
		}
	}
}

// Catches treating visible Home as identity or evaluating a foreign document.
func TestEngageHomeReadyIsNotAuthentication(t *testing.T) {
	for _, tc := range []struct {
		f    *fakeDriver
		want bool
		fail bool
	}{
		{&fakeDriver{}, true, false},
		{&fakeDriver{host: "engage.cloud.microsoft:443"}, true, false},
		{&fakeDriver{host: "login.microsoftonline.com"}, false, false},
		{&fakeDriver{host: "engage.cloud.microsoft:444"}, false, false},
		{&fakeDriver{noHome: true}, false, false},
		{&fakeDriver{wrongPage: true}, false, false},
		{&fakeDriver{fail: "home-state"}, false, true},
		{&fakeDriver{fail: "host"}, false, true},
	} {
		got, err := HomeReady(context.Background(), tc.f)
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatalf("%+v: %v %v", tc.f, got, err)
		}
		for _, event := range tc.f.events {
			if event != "host" && event != "home-state" {
				t.Fatal(event)
			}
		}
		if tc.f.host == "login.microsoftonline.com" && !reflect.DeepEqual(tc.f.events, []string{"host"}) {
			t.Fatal(tc.f.events)
		}
	}
	var nilDriver *fakeDriver
	for _, d := range []Driver{nil, nilDriver} {
		if ok, err := HomeReady(context.Background(), d); ok || err == nil {
			t.Fatal("nil accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HomeReady(ctx, &fakeDriver{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
