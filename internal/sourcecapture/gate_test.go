package sourcecapture

import (
	"context"
	"errors"
	"testing"
	"time"
)

func probeFree(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release, err := Acquire(ctx)
	if err != nil {
		return false
	}
	release()
	return true
}

func TestSourceGateCancellationAndSingleRelease(t *testing.T) {
	release, err := Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if other, err := Acquire(ctx); other != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("a second owner entered the held source lifetime")
	}
	waiting := make(chan error, 1)
	go func() {
		next, err := Acquire(context.Background())
		if err == nil {
			next()
		}
		waiting <- err
	}()
	select {
	case <-waiting:
		t.Fatal("waiter entered before owned cleanup released the gate")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release() // a repeated release must not drain another owner's permit
	if probeFree(t) {
		t.Fatal("stale release freed another owner's source lifetime")
	}
	second()
	second()
	if !probeFree(t) {
		t.Fatal("released gate stayed held")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if owner, err := Acquire(cancelled); owner != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled acquisition received a permit")
	}
	if !probeFree(t) {
		t.Fatal("cancelled acquisition leaked its permit")
	}
}

// lateCancel reports cancellation only after the gate send has already succeeded.
type lateCancel struct {
	context.Context
	calls int
}

func (c *lateCancel) Done() <-chan struct{} { return nil }

func (c *lateCancel) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestSourceGateCancellationRacingPermitReturnsIt(t *testing.T) {
	if release, err := Acquire(&lateCancel{Context: context.Background()}); release != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation that raced the permit kept it")
	}
	if !probeFree(t) {
		t.Fatal("raced cancellation leaked its permit")
	}
}
