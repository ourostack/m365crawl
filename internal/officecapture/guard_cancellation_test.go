package officecapture

import (
	"context"
	"errors"
	"testing"
)

func TestJSONCaptureCancellationAtGuardDispatchDoesNotOpenSource(t *testing.T) {
	path, _ := ordinaryFixture(t)
	guard := captureGuard
	t.Cleanup(func() { captureGuard = guard })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	captureGuard = func(_ context.Context, operation func() error) error {
		cancel()
		return operation()
	}
	called := false
	err := WithJSONCapture(ctx, DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(*JSONCapture) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled guard dispatch acquired source")
	}
}
