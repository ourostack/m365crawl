package onenote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/sourcecapture"
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
	snapshotAcquire = func(ctx context.Context) (func(), error) {
		release, err := acquire(ctx)
		if err != nil {
			return nil, err
		}
		cancel()
		return release, nil
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
	if !sourceGateFree(t) {
		t.Fatal("cancelled acquisition leaked its permit")
	}
}

func sourceGateFree(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	release, err := sourcecapture.Acquire(ctx)
	if err != nil {
		return false
	}
	release()
	return true
}

func TestCaptureIndexLockFaultsReleaseSourceLifetime(t *testing.T) {
	for _, fault := range []string{"lock", "unlock"} {
		t.Run(fault, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
			if err != nil {
				t.Fatal(err)
			}
			original := snapshotLock
			t.Cleanup(func() { snapshotLock = original })
			snapshotLock = func(file *os.File) (func() error, error) {
				if fault == "lock" {
					return nil, errors.New("lock fault")
				}
				unlock, err := original(file)
				if err != nil {
					return nil, err
				}
				return func() error { return errors.Join(unlock(), errors.New("unlock fault")) }, nil
			}
			capture, err := CaptureIndex(context.Background(), path)
			if fault == "unlock" {
				if err != nil {
					t.Fatal(err)
				}
				if err := capture.Close(); err == nil {
					t.Fatal("unlock failure hidden")
				}
			} else if err == nil || capture != nil {
				t.Fatal("lock failure admitted")
			}
			if !sourceGateFree(t) {
				t.Fatal("lock fault retained the source lifetime")
			}
			if output := sqliteWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("lock leak after fault: %s", output)
			}
		})
	}
}
