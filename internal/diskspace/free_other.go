//go:build !darwin && !linux && !windows

package diskspace

// free cannot measure free space here.
func free(string) (uint64, bool, error) { return 0, false, nil }
