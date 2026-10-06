//go:build unix

package hxstore

import (
	"os"
	"syscall"
)

// openFlags opens read-only and non-blocking, so a named pipe swapped in for the
// file cannot block the open. (O_NONBLOCK has no effect on a regular file.)
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK
