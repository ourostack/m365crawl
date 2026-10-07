//go:build !windows

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// singletonLive reports whether the browser's own SingletonLock in the profile names a live
// process. Chrome and Edge make it a symlink to "<host>-<pid>".
func singletonLive(profile string) bool {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return false
	}
	i := strings.LastIndex(target, "-")
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
