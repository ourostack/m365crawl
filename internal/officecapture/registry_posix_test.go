//go:build !windows

package officecapture

import (
	"os"
	"testing"
)

// leafCloseBeforeBound is how many handles named like the leaf an open closes before the bound one.
const leafCloseBeforeBound = 0

// heldSourceDeniesWrite reports whether err is the platform refusing a write to a held source.
// POSIX never does: the write succeeds and validation must catch it.
func heldSourceDeniesWrite(error) bool { return false }

// openDescriptors counts this process's open descriptors.
func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("no descriptor table to count")
	}
	return len(entries)
}
