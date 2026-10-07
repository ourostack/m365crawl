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

	"github.com/ourostack/m365crawl/internal/errs"
)

func stubListProcs(f func() ([]procInfo, error)) (restore func()) {
	old := listProcs
	listProcs = f
	return func() { listProcs = old }
}

func checkProfileMode(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("profile mode = %v %v, want 0700", fi, err)
	}
}

func skipStdioCheckOnWindows(*testing.T) {}

func TestLaunchSweepFails(t *testing.T) {
	restore := stubListProcs(func() ([]procInfo, error) { return nil, errors.New("no ps") })
	defer restore()
	_, _, err := launchFake(t, nil, nil)
	requireCode(t, err, errs.CodeBrowserFailed)
	if !strings.Contains(err.Error(), "an earlier browser would not stop") {
		t.Fatalf("err = %v", err)
	}
}

// startSleeper starts a process in its own group and returns its pid; it is killed at test end.
func startSleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process.Pid
}

func TestLaunchSingletonLockLive(t *testing.T) {
	live := startSleeper(t)
	_, profile, err := launchFake(t, nil, func(o *LaunchOptions) {
		if err := os.MkdirAll(o.Profile, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(o.Profile, "SingletonLock")
		if err := os.Symlink("some-host-"+strconv.Itoa(live), link); err != nil {
			t.Fatal(err)
		}
	})
	requireCode(t, err, errs.CodeBrowserBusy)
	if _, statErr := os.Stat(filepath.Join(profile, "fake-argv.txt")); statErr == nil {
		t.Fatal("nothing may be started when a live browser holds the profile")
	}
	requireLockFree(t, profile)
}

func TestSingletonLive(t *testing.T) {
	live := startSleeper(t)
	cases := []struct {
		name, target string
		want         bool
	}{
		{"live pid", "host-" + strconv.Itoa(live), true},
		{"dead pid", "host-2147483646", false},
		{"no pid", "host", false},
		{"zero pid", "host-0", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.Symlink(c.target, filepath.Join(dir, "SingletonLock")); err != nil {
			t.Fatal(err)
		}
		if got := singletonLive(dir); got != c.want {
			t.Errorf("%s: singletonLive = %v", c.name, got)
		}
	}
	if singletonLive(t.TempDir()) {
		t.Error("no lock, not live")
	}
}
