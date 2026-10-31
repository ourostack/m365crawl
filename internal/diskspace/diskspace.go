// Package diskspace measures the free space of the filesystem that holds a directory.
package diskspace

// Free reports the bytes available to an unprivileged user on the filesystem holding dir.
// measured is false when the platform cannot measure it.
func Free(dir string) (avail uint64, measured bool, err error) { return free(dir) }
