package onenote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureIndexCancellationImmediatelyAfterPermitOpensNoSource(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	acquire, open := snapshotAcquire, snapshotOpen
	t.Cleanup(func() { snapshotAcquire, snapshotOpen = acquire, open })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshotAcquire = func(ctx context.Context) error {
		if err := acquire(ctx); err != nil {
			return err
		}
		cancel()
		return nil
	}
	opens := 0
	snapshotOpen = func(name string) (*os.File, error) {
		opens++
		return open(name)
	}
	if capture, err := CaptureIndex(ctx, path); capture != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled permitted capture admitted: %v", err)
	}
	if opens != 0 {
		t.Fatal("cancelled capture opened a source descriptor")
	}
	select {
	case indexCaptureGate <- struct{}{}:
		<-indexCaptureGate
	default:
		t.Fatal("cancelled acquisition leaked its permit")
	}
}
