//go:build !windows

package browser

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

type procInfo struct {
	pid, pgid int
	argv      []string
}

// Test seams.
var (
	psCommand = "/bin/ps"
	listProcs = psList
	readArgs  = readProcArgs
)

// psList lists every process with its group and its argument vector. The vector comes from the
// kernel (kern.procargs2 on macOS, /proc/<pid>/cmdline on Linux), one element per argument, so
// a profile path with spaces in it is matched exactly. Processes whose arguments cannot be read
// (exited meanwhile, or another user's) are left out; they are not ours.
func psList() ([]procInfo, error) {
	out, err := exec.Command(psCommand, "-axo", "pid=,pgid=").Output() //nolint:gosec // G204: fixed arguments
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for _, p := range parsePS(string(out)) {
		argv, err := readArgs(p.pid)
		if err != nil || len(argv) == 0 {
			continue
		}
		p.argv = argv
		procs = append(procs, p)
	}
	return procs, nil
}

func parsePS(out string) []procInfo {
	var procs []procInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pgid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, procInfo{pid: pid, pgid: pgid})
	}
	return procs
}

// ownedBy reports whether one argument is exactly this profile's --user-data-dir flag. Matching
// whole arguments means a longer path that merely starts with the profile path does not count.
func ownedBy(argv []string, profile string) bool {
	return slices.Contains(argv, "--user-data-dir="+profile)
}

// profileProcs returns the processes started with this profile, other than this one.
func profileProcs(profile string) ([]procInfo, error) {
	all, err := listProcs()
	if err != nil {
		return nil, err
	}
	var out []procInfo
	for _, p := range all {
		if p.pid != os.Getpid() && ownedBy(p.argv, profile) {
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

// groupHasProfileProcs reports whether any process runs with the profile. When the list cannot
// be read it answers yes, so the caller errs toward cleaning up.
func groupHasProfileProcs(profile string) bool {
	procs, err := profileProcs(profile)
	return err != nil || len(procs) > 0
}
