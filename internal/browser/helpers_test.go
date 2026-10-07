package browser

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/store"
)

func init() {
	// Keep the polling loops fast in tests.
	pollEvery = 5 * time.Millisecond
	closeWait = 500 * time.Millisecond
	retryEvery = time.Millisecond
	loadPollWait = time.Millisecond
}

// newProfile returns a fresh profile path and, at the end of the test, force-kills anything
// still running with it, so a failing test cannot leak a process.
func newProfile(t *testing.T) string {
	t.Helper()
	profile := filepath.Join(t.TempDir(), "browser")
	t.Cleanup(func() { _ = sweepArgv(profile, false) })
	return profile
}

// launchFake launches the fake browser with the given FAKE_* environment.
func launchFake(t *testing.T, env map[string]string, mod func(*LaunchOptions)) (*Browser, string, error) {
	t.Helper()
	exe := browsertest.FakeBrowser(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	o := LaunchOptions{Exe: exe, Kind: KindCustom, Profile: newProfile(t), Headless: true, Timeout: 10 * time.Second}
	if mod != nil {
		mod(&o)
	}
	b, err := Launch(context.Background(), o)
	if b != nil {
		t.Cleanup(func() { _ = b.Close() })
	}
	return b, o.Profile, err
}

// fakePids reads the pids the fake wrote (leader, child, escaped), waiting for the file.
func fakePids(t *testing.T, profile string) map[string]int {
	t.Helper()
	var data []byte
	if !waitUntil(5*time.Second, func() bool {
		var err error
		data, err = os.ReadFile(filepath.Join(profile, browsertest.PidsFile)) //nolint:gosec // G304: a temp profile
		return err == nil && strings.HasSuffix(string(data), "\n") && strings.Contains(string(data), "child")
	}) {
		t.Fatal("the fake browser never wrote its pids")
	}
	out := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		n, _ := strconv.Atoi(f[1])
		out[f[0]] = n
	}
	return out
}

func requireGone(t *testing.T, name string, pid int) {
	t.Helper()
	if !waitUntil(5*time.Second, func() bool { return !processExists(pid) }) {
		t.Fatalf("%s (pid %d) is still running", name, pid)
	}
}

func requireLockFree(t *testing.T, profile string) {
	t.Helper()
	release, err := store.AcquireLock(profile)
	if err != nil {
		t.Fatalf("the profile lock was not released: %v", err)
	}
	release()
}
