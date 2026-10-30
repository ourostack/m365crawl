//go:build linux

package scoutcapture

import (
	"context"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOnlyLocalLinuxFilesystemsQualify(t *testing.T) {
	root := appFolder(t)
	swap(t, &fstatfs, func(fd int, fs *unix.Statfs_t) error {
		err := unix.Fstatfs(fd, fs)
		fs.Type = unix.FUSE_SUPER_MAGIC
		return err
	})
	if _, _, err := collect(t, Options{Root: root, MaxBytes: 100}); err == nil || err.Error() != "scout_root_unqualified" {
		t.Fatalf("%v", err)
	}
}

func TestLinuxHasNoDefaultOneDrive(t *testing.T) {
	found, err := Discover(context.Background(), t.TempDir())
	if err != nil || len(found) != 0 {
		t.Fatalf("%v %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, t.TempDir()); err == nil {
		t.Fatal("cancelled discovery ran")
	}
}
