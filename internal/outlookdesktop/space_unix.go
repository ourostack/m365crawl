//go:build darwin || linux

package outlookdesktop

import "golang.org/x/sys/unix"

// realFreeBytes reports the bytes available to an unprivileged user on the filesystem holding
// dir. ok is false when the platform cannot measure it.
func realFreeBytes(dir string) (avail uint64, ok bool, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false, err
	}
	return uint64(st.Bavail) * blockSize(&st), true, nil //nolint:gosec // G115: Bavail is unsigned
}

// freeBytes is a variable so a test can force a failure or a small answer.
var freeBytes = realFreeBytes
