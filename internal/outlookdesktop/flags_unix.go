//go:build unix

package outlookdesktop

import (
	"os"
	"syscall"
)

// sourceOpenFlags opens Outlook's store read-only and non-blocking: a FIFO swapped in for the
// file cannot block the open. (O_NONBLOCK has no effect on a regular file.)
const sourceOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
