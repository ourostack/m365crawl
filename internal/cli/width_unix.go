//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// winsize is a test seam: a pseudo-terminal is not available to a deterministic unit test.
var winsize = unix.IoctlGetWinsize

func fileWidth(f *os.File) int {
	ws, err := winsize(int(f.Fd()), unix.TIOCGWINSZ) //nolint:gosec // G115: fds fit in int
	if err != nil {
		return 0
	}
	return int(ws.Col)
}
