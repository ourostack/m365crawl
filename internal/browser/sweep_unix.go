//go:build !windows

package browser

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type procInfo struct {
	pid, pgid int
	args      string
}

// Test seams.
var (
	psCommand = "ps"
	listProcs = psList
)

// psList lists every process with its group and full command line. ps prints the arguments
// joined by spaces, which is enough to find a --user-data-dir flag.
func psList() ([]procInfo, error) {
	out, err := exec.Command(psCommand, "-axww", "-o", "pid=,pgid=,args=").Output() //nolint:gosec // G204: fixed arguments
	if err != nil {
		return nil, err
	}
	return parsePS(string(out)), nil
}

func parsePS(out string) []procInfo {
	var procs []procInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pgid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		rest := strings.TrimSpace(line)
		rest = strings.TrimSpace(strings.TrimPrefix(rest, f[0]))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, f[1]))
		procs = append(procs, procInfo{pid: pid, pgid: pgid, args: rest})
	}
	return procs
}

// ownedBy reports whether args carry this profile's --user-data-dir flag, and not a longer path
// that merely starts with it.
func ownedBy(args, profile string) bool {
	marker := "--user-data-dir=" + profile
	for from := 0; ; {
		i := strings.Index(args[from:], marker)
		if i < 0 {
			return false
		}
		end := from + i + len(marker)
		if end == len(args) || args[end] == ' ' {
			return true
		}
		from = end
	}
}

// profileProcs returns the processes started with this profile, other than this one.
func profileProcs(profile string) ([]procInfo, error) {
	all, err := listProcs()
	if err != nil {
		return nil, err
	}
	var out []procInfo
	for _, p := range all {
		if p.pid != os.Getpid() && ownedBy(p.args, profile) {
			out = append(out, p)
		}
	}
	return out, nil
}

// reclaim ends the orphan group a pid file names, but only while a member of that group still
// runs with this profile: a pid that was reused by an unrelated process is left alone.
func reclaim(profile string, pid int, _ int64) {
	procs, err := profileProcs(profile)
	if err != nil {
		return
	}
	for _, p := range procs {
		if p.pgid == pid {
			stopGroup(&group{pgid: pid})
			return
		}
	}
}

// sweepArgv ends every process started with this profile, wherever it escaped to. With polite
// set it sends SIGTERM and waits, then SIGKILL; without, SIGKILL at once. It returns an error
// when a process survives.
func sweepArgv(profile string, polite bool) error {
	signals := []syscall.Signal{syscall.SIGKILL}
	if polite {
		signals = []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}
	}
	for _, sig := range signals {
		procs, err := profileProcs(profile)
		if err != nil {
			return err
		}
		if len(procs) == 0 {
			return nil
		}
		for _, p := range procs {
			_ = syscall.Kill(p.pid, sig)
		}
		grace := stopGrace
		if sig == syscall.SIGKILL {
			grace = killGrace
		}
		waitUntil(grace, func() bool {
			left, err := profileProcs(profile)
			return err == nil && len(left) == 0
		})
	}
	left, err := profileProcs(profile)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		return fmt.Errorf("%d browser processes survived the kill", len(left))
	}
	return nil
}
