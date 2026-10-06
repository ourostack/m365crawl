//go:build darwin

package outlookdesktop

import "golang.org/x/sys/unix"

// blockSize is the unit of Bavail; on macOS that is Bsize.
func blockSize(st *unix.Statfs_t) uint64 { return uint64(st.Bsize) }
