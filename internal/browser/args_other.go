//go:build !windows && !darwin && !linux

package browser

import (
	"os/exec"
	"strconv"
	"strings"
)

// Other Unix systems: ps joins the arguments with spaces, so a profile path with spaces in it
// cannot be matched exactly there.
func readProcArgs(pid int) ([]string, error) {
	out, err := exec.Command(psCommand, "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: fixed arguments
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}
