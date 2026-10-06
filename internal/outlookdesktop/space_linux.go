//go:build linux

package outlookdesktop

import "golang.org/x/sys/unix"

// blockSize is the unit of Bavail. On Linux that is the fragment size, not Bsize (the preferred
// I/O size, which can be larger).
func blockSize(st *unix.Statfs_t) uint64 { return uint64(st.Frsize) } //nolint:gosec // G115: a size is non-negative
