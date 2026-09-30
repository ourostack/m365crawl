//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

func fileWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ) //nolint:gosec // G115: fds fit in int
	if err != nil {
		return 0
	}
	return int(ws.Col)
}
