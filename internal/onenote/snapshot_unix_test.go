//go:build darwin || linux

package onenote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCaptureIndexLockFaultsReleaseNativeProtection(t *testing.T) {
	for _, failCall := range []int{1, 2, 3, 4} {
		t.Run(string(rune('0'+failCall)), func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
			if err != nil {
				t.Fatal(err)
			}
			original := snapshotFcntl
			t.Cleanup(func() { snapshotFcntl = original })
			calls := 0
			snapshotFcntl = func(fd uintptr, cmd int, lock *unix.Flock_t) error {
				calls++
				if calls == failCall {
					return errors.New("lock fault")
				}
				return original(fd, cmd, lock)
			}
			capture, err := CaptureIndex(context.Background(), path)
			if failCall == 4 {
				if err != nil {
					t.Fatal(err)
				}
				if err := capture.Close(); err == nil {
					t.Fatal("unlock failure hidden")
				}
			} else if err == nil || capture != nil {
				t.Fatal("lock failure admitted")
			}
			snapshotFcntl = original
			if output := sqliteWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("lock leak after fault: %s", output)
			}
		})
	}
}

func TestCaptureIndexHardLinkRefused(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".alias"); err != nil {
		t.Fatal(err)
	}
	if capture, err := CaptureIndex(context.Background(), path); capture != nil || err == nil {
		t.Fatal("multiply linked source admitted")
	}
}

type unsupportedSnapshotInfo struct{ os.FileInfo }

func (unsupportedSnapshotInfo) Sys() any { return nil }

func TestSnapshotNativeIdentityFailures(t *testing.T) {
	path := indexFixture(t, "index.db")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshotSingleLink(file); err == nil {
		t.Fatal("closed descriptor identity accepted")
	}
	if identity := snapshotIdentity(unsupportedSnapshotInfo{info}); identity != "" {
		t.Fatal("unsupported identity fabricated")
	}
}
