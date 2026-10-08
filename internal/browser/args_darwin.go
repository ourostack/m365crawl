//go:build darwin

package browser

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func readProcArgs(pid int) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcArgs(buf)
}

var errProcArgs = errors.New("unreadable process arguments")

// parseProcArgs decodes a kern.procargs2 buffer: an int argc, the executable path, NUL padding,
// then argc NUL-terminated arguments (the environment follows and is ignored).
func parseProcArgs(buf []byte) ([]string, error) {
	if len(buf) < 4 {
		return nil, errProcArgs
	}
	argc := int(int32(binary.NativeEndian.Uint32(buf))) //nolint:gosec // G115: the kernel stores argc as a C int
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, errProcArgs
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	parts := bytes.Split(rest, []byte{0})
	if argc < 0 || argc > len(parts) {
		return nil, errProcArgs
	}
	argv := make([]string, argc)
	for k := range argv {
		argv[k] = string(parts[k])
	}
	return argv, nil
}
