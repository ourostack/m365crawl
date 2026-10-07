//go:build !windows

package browser

import (
	"context"
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

// startHolder starts a process whose arguments carry the profile's --user-data-dir, like a
// running browser. It blocks reading stdin and is killed at test end.
func startHolder(t *testing.T, profile string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "read x", "sh", "--user-data-dir="+profile) //nolint:gosec // G204: test helper
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if !waitUntil(5*time.Second, func() bool { p, err := profileProcs(profile); return err == nil && len(p) > 0 }) {
		t.Fatal("the holder is not visible to the process list")
	}
	return cmd.Process.Pid
}

func linkSingleton(t *testing.T, profile, target string) string {
	t.Helper()
	makePrivateParent(t, profile)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(profile, "SingletonLock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func thisHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLaunchSingletonLockLive(t *testing.T) {
	// A running browser holds the profile: its SingletonLock names its pid, and its arguments
	// carry the profile. (The orphan sweep is stubbed out: it would end such a process, and
	// this test is about a holder the sweep could not reach.)
	old := sweepOrphan
	t.Cleanup(func() { sweepOrphan = old })
	sweepOrphan = func(string) error { return nil }
	profile := newProfile(t)
	holder := startHolder(t, profile)
	link := linkSingleton(t, profile, thisHost(t)+"-"+strconv.Itoa(holder))
	exe := browsertest.FakeBrowser(t)
	_, err := Launch(context.Background(), LaunchOptions{Exe: exe, Profile: profile, Headless: true})
	requireCode(t, err, errs.CodeBrowserBusy)
	if _, statErr := os.Stat(filepath.Join(profile, "fake-argv.txt")); statErr == nil {
		t.Fatal("nothing may be started when a live browser holds the profile")
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("a live holder's lock must be left alone")
	}
	requireLockFree(t, profile)
}

func TestLaunchStaleSingletonLockOfReusedPid(t *testing.T) {
	// The browser we killed left its lock behind, and its pid now belongs to an unrelated
	// process. That must not block the launch; the stale link is removed.
	profile := newProfile(t)
	other := startSleeper(t)
	link := linkSingleton(t, profile, thisHost(t)+"-"+strconv.Itoa(other))
	exe := browsertest.FakeBrowser(t)
	b, err := Launch(context.Background(), LaunchOptions{Exe: exe, Profile: profile, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := os.Readlink(link); err == nil {
		t.Fatal("the stale SingletonLock must be removed")
	}
	if !processExists(other) {
		t.Fatal("the unrelated process must be left alone")
	}
}

func TestSingletonLive(t *testing.T) {
	profile := newProfile(t)
	host := thisHost(t)
	holder := startHolder(t, profile)
	other := startSleeper(t)
	cases := []struct {
		name, target string
		want         bool
	}{
		{"holder runs with the profile", host + "-" + strconv.Itoa(holder), true},
		{"hyphenated host", "a-b-" + strconv.Itoa(holder), false},
		{"live pid of an unrelated process", host + "-" + strconv.Itoa(other), false},
		{"dead pid", host + "-2147483646", false},
		{"other host", "elsewhere-" + strconv.Itoa(holder), false},
		{"no pid", "host", false},
		{"bad pid", host + "-x", false},
		{"zero pid", host + "-0", false},
	}
	for _, c := range cases {
		link := linkSingleton(t, profile, c.target)
		if got := singletonLive(profile); got != c.want {
			t.Errorf("%s: singletonLive = %v", c.name, got)
		}
		if _, err := os.Readlink(link); (err == nil) != c.want {
			t.Errorf("%s: the link must stay only while its holder is live", c.name)
		}
		_ = os.Remove(link)
	}
	if singletonLive(profile) {
		t.Error("no lock, not live")
	}
}

func TestSingletonLiveUnknownHostName(t *testing.T) {
	old := hostname
	t.Cleanup(func() { hostname = old })
	hostname = func() (string, error) { return "", errors.New("no host name") }
	profile := newProfile(t)
	holder := startHolder(t, profile)
	linkSingleton(t, profile, "anything-"+strconv.Itoa(holder))
	if !singletonLive(profile) {
		t.Fatal("without a host name to compare, the process check alone decides")
	}
}

func TestSingletonLiveWhenProcessListFails(t *testing.T) {
	profile := newProfile(t)
	link := linkSingleton(t, profile, thisHost(t)+"-1")
	defer stubListProcs(func() ([]procInfo, error) { return nil, errors.New("no ps") })()
	if !singletonLive(profile) {
		t.Fatal("when the holder cannot be checked, assume it is live")
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("an unchecked lock must not be removed")
	}
}
