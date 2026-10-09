//go:build windows

package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"golang.org/x/sys/windows"
)

func inJob(t *testing.T, pid int, job windows.Handle) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("OpenProcess(%d): %v", pid, err)
	}
	defer windows.CloseHandle(h) //nolint:errcheck // test helper
	var result int32
	r, _, err := procIsProcessInJob.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		t.Fatal(err)
	}
	return result != 0
}

// x/sys/windows does not wrap IsProcessInJob.
var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// The fake starts its child as its very first act. Both being in the job proves the leader was
// put in the job while still suspended, before it could start anything outside it.
func TestJobAssignedBeforeResume(t *testing.T) {
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	if !inJob(t, pids["leader"], b.group.job) || !inJob(t, pids["child"], b.group.job) {
		t.Fatal("the browser and its child must both be in the job object")
	}
	if !b.group.alive() {
		t.Fatal("the job has live members")
	}
}

func startOrphanWindows(t *testing.T, profile string) (*exec.Cmd, map[string]int) {
	t.Helper()
	exe := browsertest.FakeBrowser(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, Argv(LaunchOptions{Profile: profile, Headless: true})...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	pids := fakePids(t, profile)
	t.Cleanup(func() {
		for _, pid := range pids {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
	return cmd, pids
}

func TestSweepOrphanWindows(t *testing.T) {
	profile := newProfile(t)
	cmd, pids := startOrphanWindows(t, profile)
	if err := writePidFile(profile, cmd.Process.Pid, creationTime(uint32(cmd.Process.Pid))); err != nil {
		t.Fatal(err)
	}
	if err := SweepOrphan(profile); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"]) // outside any job: only the command-line sweep reaches it
	requireNoProfileProcs(t, profile)
	if _, err := os.Stat(pidPath(profile)); !os.IsNotExist(err) {
		t.Fatal("the pid file must be removed")
	}
}

// A pid file whose pid now belongs to another process, one that does not run with this profile,
// leaves that process alone.
func TestSweepOrphanWindowsIgnoresReusedPid(t *testing.T) {
	profile := newProfile(t)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	other := newProfile(t)
	cmd, pids := startOrphanWindows(t, other)
	for _, started := range []int64{0, 12345} { // unknown, or the creation time of another process
		if err := writePidFile(profile, cmd.Process.Pid, started); err != nil {
			t.Fatal(err)
		}
		if err := SweepOrphan(profile); err != nil {
			t.Fatal(err)
		}
		if !processExists(pids["leader"]) {
			t.Fatalf("a process with start %d that is not ours was killed", started)
		}
	}
	// A pid that no longer exists is simply forgotten.
	if err := writePidFile(profile, 0x7ffffff0, 1); err != nil {
		t.Fatal(err)
	}
	if err := SweepOrphan(profile); err != nil {
		t.Fatal(err)
	}
}

// After a handoff, the process that runs the browser is the launcher's child, so it is in the job.
func TestHandoffSuccessorIsInTheJob(t *testing.T) {
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvLateHandoff: "1", browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	if !inJob(t, pids["leader"], b.group.job) || !inJob(t, pids["child"], b.group.job) {
		t.Fatal("the handed-off browser and its child must both be in the job object")
	}
}

func TestSingletonLiveWindows(t *testing.T) {
	dir := t.TempDir()
	if singletonLive(dir) {
		t.Fatal("no lockfile, not live")
	}
	f, err := os.Create(filepath.Join(dir, "lockfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !singletonLive(dir) {
		t.Fatal("a held lockfile means a live browser")
	}
	_ = f.Close()
	if singletonLive(dir) {
		t.Fatal("a stale lockfile nobody holds is not live")
	}
}
