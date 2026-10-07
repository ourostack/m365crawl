//go:build !windows

package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// hostname is a test seam; Chrome and Edge write the machine's host name into the lock.
var hostname = os.Hostname

// singletonLive reports whether a live browser holds this profile. Chrome and Edge make
// SingletonLock a symlink to "<host>-<pid>", and a browser we killed leaves it behind, so a
// pid alone proves nothing: the pid may have been reused by an unrelated process. The lock
// counts only when the link names this machine and that pid runs with this profile's
// --user-data-dir. Any other lock is stale, and is removed so the next browser starts clean.
func singletonLive(profile string) bool {
	link := filepath.Join(profile, "SingletonLock")
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if lockHolderRuns(target, profile) {
		return true
	}
	_ = os.Remove(link)
	return false
}

func lockHolderRuns(target, profile string) bool {
	i := strings.LastIndex(target, "-")
	if i < 0 {
		return false
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return false
	}
	if host, err := hostname(); err == nil && host != target[:i] {
		return false
	}
	procs, err := listProcs()
	if err != nil {
		return true // cannot tell, so do not start a second browser on the profile
	}
	for _, p := range procs {
		if p.pid == pid {
			return ownedBy(p.argv, profile)
		}
	}
	return false
}
