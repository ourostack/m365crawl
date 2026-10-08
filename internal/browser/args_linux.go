//go:build linux

package browser

import (
	"bytes"
	"fmt"
	"os"
)

func readProcArgs(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	var argv []string
	for _, a := range bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0}) {
		argv = append(argv, string(a))
	}
	return argv, nil
}
