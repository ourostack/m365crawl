//go:build darwin || linux

package diskspace

import "golang.org/x/sys/unix"

func free(dir string) (uint64, bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false, err
	}
	return uint64(st.Bavail) * blockSize(&st), true, nil //nolint:gosec // G115: Bavail is unsigned
}
