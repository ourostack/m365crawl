package browser

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
)

func TestCloseKillsGroup(t *testing.T) {
	// The fake ignores Browser.close, so only the group kill can end it and its child.
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
	requireNoProfileProcs(t, profile)
	if _, statErr := os.Stat(pidPath(profile)); !os.IsNotExist(statErr) {
		t.Fatal("the pid file must be removed")
	}
	requireLockFree(t, profile)
}

func TestCloseAfterBrowserClose(t *testing.T) {
	b, profile, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Page(context.Background()); err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
}

func TestCloseIdempotent(t *testing.T) {
	b, _, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e1 := b.Close()
	e2 := b.Close()
	if e1 != nil || e2 != nil {
		t.Fatalf("Close = %v, %v", e1, e2)
	}
}

func TestCloseWhenDialFails(t *testing.T) {
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.wsURL = "ws://example.com:1/devtools/browser/x" // refused: not on this machine
	pids := fakePids(t, profile)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
}

func TestCloseTimesOutWaitingForExit(t *testing.T) {
	old := closeWait
	t.Cleanup(func() { closeWait = old })
	closeWait = 100 * time.Millisecond
	b, _, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Close took too long")
	}
}

func TestKillAll(t *testing.T) {
	KillAll() // nothing registered is a no-op
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	KillAll()
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
	if err := b.Close(); err != nil {
		t.Fatalf("Close after KillAll: %v", err)
	}
	registry.Lock()
	n := len(registry.live)
	registry.Unlock()
	if n != 0 {
		t.Fatalf("%d browsers still registered", n)
	}
}

func TestPidFile(t *testing.T) {
	dir := t.TempDir()
	if err := writePidFile(dir, 42, 7); err != nil {
		t.Fatal(err)
	}
	if pid, started, ok := readPidFile(dir); !ok || pid != 42 || started != 7 {
		t.Fatalf("read back %d %d %v", pid, started, ok)
	}
	for _, bad := range []string{"", "42", "x 7", "42 y", "0 7", "-1 7", "1 2 3"} {
		if err := os.WriteFile(pidPath(dir), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := readPidFile(dir); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
	if _, _, ok := readPidFile(t.TempDir()); ok {
		t.Error("a missing file is not a pid file")
	}
}

func TestWaitUntil(t *testing.T) {
	if !waitUntil(time.Second, func() bool { return true }) {
		t.Fatal("true at once")
	}
	if waitUntil(30*time.Millisecond, func() bool { return false }) {
		t.Fatal("never true")
	}
}

func TestCloseDoesNotNeedAContext(t *testing.T) {
	// Close must work after the launch context is gone: it uses its own deadline.
	exe := browsertest.FakeBrowser(t)
	profile := newProfile(t)
	ctx, cancel := context.WithCancel(context.Background())
	b, err := Launch(ctx, LaunchOptions{Exe: exe, Profile: profile, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := b.Close(); err != nil {
		t.Fatal(errors.Join(err))
	}
	requireNoProfileProcs(t, profile)
}

func TestReleasedGroupIsInert(t *testing.T) {
	b, _, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b.group.term()
	b.group.kill()
	if b.group.alive() {
		t.Fatal("a released group must report nothing alive and signal nothing")
	}
}
