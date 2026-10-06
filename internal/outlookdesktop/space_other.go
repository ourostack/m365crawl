//go:build !darwin && !linux

package outlookdesktop

// realFreeBytes cannot measure free space here; ok=false makes the copy skip the space check.
func realFreeBytes(string) (avail uint64, ok bool, err error) { return 0, false, nil }

// freeBytes is a variable so a test can force a failure or a small answer.
var freeBytes = realFreeBytes
