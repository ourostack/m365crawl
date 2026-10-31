//go:build linux

package scoutcapture

import (
	"context"

	"golang.org/x/sys/unix"
)

var fstatfs = unix.Fstatfs

// localFilesystems are the filesystem types read as ordinary local disks. A network or FUSE
// filesystem, where a read can fetch from a service, is refused.
var localFilesystems = map[int64]bool{
	unix.EXT4_SUPER_MAGIC: true, unix.XFS_SUPER_MAGIC: true, unix.BTRFS_SUPER_MAGIC: true, unix.TMPFS_MAGIC: true,
	unix.OVERLAYFS_SUPER_MAGIC: true, unix.F2FS_SUPER_MAGIC: true,
}

// withGuard runs operation: Linux has no placeholder files to guard against.
func withGuard(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

func qualifiedFS(fd int) bool {
	var fs unix.Statfs_t
	if fstatfs(fd, &fs) != nil {
		return false
	}
	return localFilesystems[int64(fs.Type)] //nolint:unconvert // the field's type differs by architecture
}

func dataless(*unix.Stat_t) bool { return false }

func blocked(error) bool { return false }

// oneDriveRoots is empty: Linux has no OneDrive app, so a folder must be named with --root.
func oneDriveRoots(string) []string { return nil }
