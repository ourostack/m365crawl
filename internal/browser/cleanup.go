package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pidFileName   = "m365crawl.pid"
	devToolsFile  = "DevToolsActivePort"
	termGrace     = 2 * time.Second
	waitPollEvery = 20 * time.Millisecond
)

// Test seam: how long a stop waits after each signal.
var (
	stopGrace = termGrace
	killGrace = 2 * time.Second
)

func pidPath(profile string) string { return filepath.Join(profile, pidFileName) }

// writePidFile records the launched leader so a later run can find an orphan: the pid, then
// a start marker that tells the process from a later one that reused the pid.
func writePidFile(profile string, pid int, started int64) error {
	return os.WriteFile(pidPath(profile), []byte(fmt.Sprintf("%d %d\n", pid, started)), 0o600)
}

func readPidFile(profile string) (pid int, started int64, ok bool) {
	b, err := os.ReadFile(pidPath(profile)) //nolint:gosec // G304: inside the profile this run owns
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return 0, 0, false
	}
	p, err1 := strconv.Atoi(f[0])
	s, err2 := strconv.ParseInt(f[1], 10, 64)
	if err1 != nil || err2 != nil || p <= 0 {
		return 0, 0, false
	}
	return p, s, true
}

// waitUntil polls cond until it is true or the timeout passes, and reports whether it became true.
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(waitPollEvery)
	}
}

// stopGroup ends every process in the group: a polite signal first, then a forced one.
func stopGroup(g *group) {
	if !g.alive() {
		return
	}
	g.term()
	if waitUntil(stopGrace, func() bool { return !g.alive() }) {
		return
	}
	g.kill()
	waitUntil(killGrace, func() bool { return !g.alive() })
}

// SweepOrphan ends a browser that an earlier m365crawl run left behind in this profile: the
// process group the pid file names (when one of its members runs with this profile), then any
// other process started with this profile's --user-data-dir. It removes the pid file.
func SweepOrphan(profile string) error {
	if pid, started, ok := readPidFile(profile); ok {
		reclaim(profile, pid, started)
	}
	err := sweepArgv(profile, true)
	_ = os.Remove(pidPath(profile))
	return err
}

// registry is every browser this process launched and has not closed, for KillAll.
var registry = struct {
	sync.Mutex
	live map[*Browser]struct{}
}{live: map[*Browser]struct{}{}}

func register(b *Browser) {
	registry.Lock()
	defer registry.Unlock()
	registry.live[b] = struct{}{}
}

func unregister(b *Browser) {
	registry.Lock()
	defer registry.Unlock()
	delete(registry.live, b)
}

// KillAll force-kills every browser this process launched. It does not wait for a polite exit,
// so it is safe on the force-quit path just before the process exits.
func KillAll() {
	registry.Lock()
	var all []*Browser
	for b := range registry.live {
		all = append(all, b)
	}
	registry.Unlock()
	for _, b := range all {
		b.group.kill()
		_ = sweepArgv(b.profile, false)
	}
}
