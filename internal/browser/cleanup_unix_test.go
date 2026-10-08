//go:build !windows

package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

// startOrphan runs the fake browser the way an earlier, killed m365crawl run would have left it:
// alone, in its own process group, with no parent that cares.
func startOrphan(t *testing.T, profile string) *exec.Cmd {
	t.Helper()
	exe := browsertest.FakeBrowser(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, Argv(LaunchOptions{Profile: profile, Headless: true})...) //nolint:gosec // G204: the test binary
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

func TestSweepOrphan(t *testing.T) {
	profile := newProfile(t)
	cmd := startOrphan(t, profile)
	go func() { _ = cmd.Wait() }()
	pids := fakePids(t, profile)
	if err := writePidFile(profile, cmd.Process.Pid, 1); err != nil {
		t.Fatal(err)
	}
	if err := SweepOrphan(profile); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
	if _, err := os.Stat(pidPath(profile)); !os.IsNotExist(err) {
		t.Fatal("the pid file must be removed")
	}
}

func TestSweepOrphanLeaderGone(t *testing.T) {
	// The leader exits (a crashed helper tree); its child lives on in the leader's group.
	t.Setenv(browsertest.EnvLeaderExits, "1")
	profile := newProfile(t)
	cmd := startOrphan(t, profile)
	pids := fakePids(t, profile)
	_ = cmd.Wait()
	requireGone(t, "leader", pids["leader"])
	if !processExists(pids["child"]) {
		t.Fatal("the child must outlive the leader for this test")
	}
	if err := writePidFile(profile, pids["leader"], 1); err != nil {
		t.Fatal(err)
	}
	if err := SweepOrphan(profile); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "child", pids["child"])
}

func TestSweepOrphanIgnoresReusedPid(t *testing.T) {
	profile := newProfile(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	other := startSleeper(t) // an unrelated process in its own group, started without the profile
	if err := writePidFile(profile, other, 1); err != nil {
		t.Fatal(err)
	}
	if err := SweepOrphan(profile); err != nil {
		t.Fatal(err)
	}
	if !processExists(other) {
		t.Fatal("an unrelated process that reused the pid was killed")
	}
	if _, err := os.Stat(pidPath(profile)); !os.IsNotExist(err) {
		t.Fatal("the stale pid file must be removed")
	}
}

func TestSweepOrphanListFails(t *testing.T) {
	profile := newProfile(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePidFile(profile, 1, 1); err != nil {
		t.Fatal(err)
	}
	defer stubListProcs(func() ([]procInfo, error) { return nil, errors.New("no ps") })()
	if err := SweepOrphan(profile); err == nil {
		t.Fatal("an unreadable process list is an error")
	}
}

func TestCloseKillsEscapedChild(t *testing.T) {
	// A child that left the group (its own session) is found by its command line.
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvEscape: "1", browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fakePids(t, profile)
	waitUntil(5*time.Second, func() bool { return len(fakePidsNow(profile)) == 3 })
	pids := fakePids(t, profile)
	if pids["escaped"] == 0 {
		t.Fatal("the fake did not start an escaped child")
	}
	pgid, _ := syscall.Getpgid(pids["escaped"])
	if pgid == pids["leader"] {
		t.Fatal("the child did not escape the group")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "escaped child", pids["escaped"])
	requireGone(t, "leader", pids["leader"])
}

func fakePidsNow(profile string) map[string]int {
	b, err := os.ReadFile(profile + "/" + browsertest.PidsFile) //nolint:gosec // G304: a temp profile
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			n, _ := strconv.Atoi(f[1])
			out[f[0]] = n
		}
	}
	return out
}

func TestCloseEscalatesToKill(t *testing.T) {
	old := stopGrace
	t.Cleanup(func() { stopGrace = old })
	stopGrace = 150 * time.Millisecond
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1", browsertest.EnvIgnoreTerm: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
}

func TestCloseReportsSurvivor(t *testing.T) {
	b, profile, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := killGraceForTest(100 * time.Millisecond)
	defer old()
	// A process that cannot be killed (it does not exist) but is listed as running.
	defer stubListProcs(func() ([]procInfo, error) {
		return []procInfo{{pid: 2147483646, pgid: 2147483646, argv: []string{"x", "--user-data-dir=" + profile}}}, nil
	})()
	if err := b.Close(); err == nil {
		t.Fatal("a process that survives the kill must be reported")
	}
}

func TestSweepArgvListFailsLater(t *testing.T) {
	profile := "/p"
	calls := 0
	defer stubListProcs(func() ([]procInfo, error) {
		calls++
		if calls == 1 {
			return []procInfo{{pid: 2147483646, argv: []string{"--user-data-dir=/p"}}}, nil
		}
		return nil, errors.New("gone")
	})()
	defer killGraceForTest(50 * time.Millisecond)()
	if err := sweepArgv(profile, false); err == nil {
		t.Fatal("a list failure after the signal is an error")
	}
	calls = 0
	defer stubListProcs(func() ([]procInfo, error) {
		calls++
		if calls <= 3 {
			return []procInfo{{pid: 2147483646, argv: []string{"--user-data-dir=/p"}}}, nil
		}
		return nil, errors.New("gone")
	})()
	if err := sweepArgv(profile, true); err == nil {
		t.Fatal("a list failure at the end is an error")
	}
}

func TestSweepArgvFinalListFails(t *testing.T) {
	calls := 0
	defer stubListProcs(func() ([]procInfo, error) {
		calls++
		if calls >= 3 {
			return nil, errors.New("gone")
		}
		return []procInfo{{pid: 2147483646, argv: []string{"--user-data-dir=/p"}}}, nil
	})()
	defer killGraceForTest(0)()
	if err := sweepArgv("/p", false); err == nil {
		t.Fatal("a list failure on the final check is an error")
	}
}

func killGraceForTest(d time.Duration) (restore func()) {
	oldT, oldK := stopGrace, killGrace
	stopGrace, killGrace = d, d
	return func() { stopGrace, killGrace = oldT, oldK }
}

func TestOwnedBy(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"edge", "--user-data-dir=/a/browser", "--headless=new", "about:blank"}, true},
		{[]string{"edge", "--user-data-dir=/a/browser2", "--x"}, false},
		{[]string{"edge", "--user-data-dir=/a/browser2", "--user-data-dir=/a/browser"}, true},
		{[]string{"edge", "--user-data-dir=/a/other"}, false},
		{[]string{"tail", "-f", "/a/browser/log"}, false},
		{[]string{"x--user-data-dir=/a/browser"}, false},
		{[]string{"edge", "--user-data-dir=/a/browser /extra"}, false},
	}
	for _, c := range cases {
		if got := ownedBy(c.argv, "/a/browser"); got != c.want {
			t.Errorf("ownedBy(%q) = %v", c.argv, got)
		}
	}
	if !ownedBy([]string{"edge", "--user-data-dir=/a/my profile/x"}, "/a/my profile/x") {
		t.Error("a profile path with spaces matches as one argument")
	}
}

func TestParsePS(t *testing.T) {
	out := "  10    10\n  11 10\nbad line here\n  x 3\n  4 z\n\n  5\n"
	got := parsePS(out)
	if len(got) != 2 || got[0].pid != 10 || got[0].pgid != 10 || got[1].pid != 11 || got[1].pgid != 10 {
		t.Fatalf("parsePS = %+v", got)
	}
}

func TestPsListReal(t *testing.T) {
	procs, err := psList()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range procs {
		found = found || (p.pid == os.Getpid() && len(p.argv) > 0)
	}
	if !found {
		t.Fatal("ps did not list this process with its arguments")
	}
	old := psCommand
	t.Cleanup(func() { psCommand = old })
	psCommand = "/nonexistent/ps"
	if _, err := psList(); err == nil {
		t.Fatal("a missing ps is an error")
	}
}

func TestPsListSkipsUnreadableProcesses(t *testing.T) {
	old := readArgs
	t.Cleanup(func() { readArgs = old })
	readArgs = func(pid int) ([]string, error) {
		switch pid % 3 {
		case 0:
			return nil, errors.New("gone")
		case 1:
			return nil, nil
		}
		return []string{"x"}, nil
	}
	procs, err := psList()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.pid%3 != 2 {
			t.Fatalf("process %d should have been skipped", p.pid)
		}
	}
}

func TestPsPathIsAbsolute(t *testing.T) {
	if !filepath.IsAbs(psCommand) {
		t.Fatalf("ps is run by absolute path, not PATH lookup: %s", psCommand)
	}
}

// A profile path with spaces is matched as one argument, and a sibling path is not.
func TestProfileProcsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "my profile", "browser")
	sibling := profile + "2"
	start := func(arg string) {
		cmd := exec.Command("sh", "-c", "read x", "sh", arg) //nolint:gosec // G204: test helper
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	start("--user-data-dir=" + profile)
	start("--user-data-dir=" + sibling)
	if !waitUntil(5*time.Second, func() bool {
		p, err := profileProcs(profile)
		return err == nil && len(p) == 1
	}) {
		t.Fatal("exactly the process with the exact profile argument must match")
	}
}

func TestProfileProcsSkipsSelfAndListError(t *testing.T) {
	defer stubListProcs(func() ([]procInfo, error) {
		return []procInfo{{pid: os.Getpid(), argv: []string{"--user-data-dir=/p"}}, {pid: 5, argv: []string{"--user-data-dir=/p"}}, {pid: 6, argv: []string{"x"}}}, nil
	})()
	procs, err := profileProcs("/p")
	if err != nil || len(procs) != 1 || procs[0].pid != 5 {
		t.Fatalf("procs = %+v %v", procs, err)
	}
}

func TestSweepArgvForcedKillSucceeds(t *testing.T) {
	calls := 0
	defer stubListProcs(func() ([]procInfo, error) {
		calls++
		if calls == 1 {
			return []procInfo{{pid: 2147483646, argv: []string{"--user-data-dir=/p"}}}, nil
		}
		return nil, nil
	})()
	defer killGraceForTest(0)()
	if err := sweepArgv("/p", false); err != nil {
		t.Fatal(err)
	}
}

func TestStopSkipsGroupSignalWhenLeaderGoneAndNothingRuns(t *testing.T) {
	bystander := startSleeper(t) // leads its own group; the id stands for a reused group id
	profile := "/p"
	newBrowser := func(leaderExited bool) *Browser {
		b := &Browser{profile: profile, group: &group{pgid: bystander}, exited: make(chan struct{}), release: func() {}}
		if leaderExited {
			close(b.exited)
		}
		return b
	}
	defer stubListProcs(func() ([]procInfo, error) { return nil, nil })()
	if err := newBrowser(true).stop(); err != nil {
		t.Fatal(err)
	}
	if !processExists(bystander) {
		t.Fatal("the group was signalled although the leader had exited and nothing ran with the profile")
	}
	// With processes still running under the profile the group is stopped as before.
	stubListProcs(func() ([]procInfo, error) {
		return []procInfo{{pid: bystander, pgid: bystander, argv: []string{"--user-data-dir=" + profile}}}, nil
	})
	if err := newBrowser(true).stop(); err == nil {
		t.Log("stop reported no survivor")
	}
	requireGone(t, "group", bystander)
}

func TestWorthSignallingGroupWhenListFails(t *testing.T) {
	defer stubListProcs(func() ([]procInfo, error) { return nil, errors.New("no ps") })()
	b := &Browser{profile: "/p", exited: make(chan struct{})}
	close(b.exited)
	if !b.worthSignallingGroup() {
		t.Fatal("when the process list cannot be read, clean up")
	}
}
