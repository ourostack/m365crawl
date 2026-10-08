package browser

import (
	"context"
	"errors"
	"os"
	"sync"
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

func TestCloseDuringHungPage(t *testing.T) {
	// The page never answers and the caller set no deadline. Close, from another goroutine, must
	// still end the browser within its grace periods, and the stuck call must come back.
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1", browsertest.EnvHangEval: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pids := fakePids(t, profile)
	page, err := b.Page(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evalDone := make(chan error, 1)
	go func() { evalDone <- page.Eval(context.Background(), "1", nil) }()
	time.Sleep(200 * time.Millisecond) // let the call reach the browser

	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Close did not return while a page call was hung")
	}
	requireGone(t, "leader", pids["leader"])
	requireGone(t, "child", pids["child"])
	select {
	case err := <-evalDone:
		if err == nil {
			t.Fatal("the hung call must fail once the browser is gone")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the hung call never returned")
	}
}

func TestKillAllRacesClose(t *testing.T) {
	b, _, err := launchFake(t, map[string]string{browsertest.EnvIgnoreClose: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); KillAll() }()
		go func() { defer wg.Done(); _ = b.Close() }()
	}
	wg.Wait()
}

func TestConnectUsesTheConnectionAnotherCallerMade(t *testing.T) {
	s := browsertest.NewServer(t)
	b := testBrowser(s)
	winner := &client{}
	old := dialFn
	t.Cleanup(func() { dialFn = old })
	dialFn = func(ctx context.Context, u string) (*client, error) {
		b.mu.Lock()
		b.conn = winner // another caller connected while this one was dialing
		b.mu.Unlock()
		return old(ctx, u)
	}
	c, err := b.connect(context.Background())
	if err != nil || c != winner {
		t.Fatalf("connect = %p %v, want the first connection %p", c, err, winner)
	}
}
