//go:build darwin || linux

package onenote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
	file, err := os.Open(path) //nolint:gosec // opens only the synthetic fixture created immediately above
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
