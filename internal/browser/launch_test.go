package browser

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func readFakeFile(t *testing.T, profile, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(profile, name)) //nolint:gosec // G304: a temp profile
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestArgv(t *testing.T) {
	got := Argv(LaunchOptions{Profile: "/p", Headless: true})
	want := append([]string{"--user-data-dir=/p"}, strings.Fields(LaunchFlags)...)
	want = append(want, "--headless=new", "about:blank")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	headed := Argv(LaunchOptions{Profile: "/p", StartURL: "https://example.test/"})
	for _, a := range headed {
		if strings.HasPrefix(a, "--headless") {
			t.Fatalf("headed argv has %s", a)
		}
	}
	if headed[len(headed)-1] != "https://example.test/" {
		t.Fatalf("start url is last: %v", headed)
	}
	for _, f := range []string{"--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-sync", "--disable-crash-reporter"} {
		if !strings.Contains(LaunchFlags, f) {
			t.Errorf("LaunchFlags lacks %s", f)
		}
	}
}

func TestLaunchHeadlessArgs(t *testing.T) {
	b, profile, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(readFakeFile(t, profile, browsertest.ArgvFile), "\n")
	for _, want := range []string{"--headless=new", "--remote-debugging-port=0", "--user-data-dir=" + profile, "--disable-crash-reporter"} {
		found := false
		for _, a := range args {
			found = found || a == want
		}
		if !found {
			t.Errorf("argv lacks %s: %v", want, args)
		}
	}
	if args[len(args)-1] != "about:blank" {
		t.Errorf("last arg = %q", args[len(args)-1])
	}
	if b.Kind != KindCustom {
		t.Errorf("kind = %s", b.Kind)
	}
	if got := readFakeFile(t, profile, pidFileName); !strings.HasPrefix(got, "") || len(strings.Fields(got)) != 2 {
		t.Errorf("pid file = %q", got)
	}
}

func TestLaunchHeadedHasNoHeadlessFlag(t *testing.T) {
	_, profile, err := launchFake(t, nil, func(o *LaunchOptions) { o.Headless = false })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFakeFile(t, profile, browsertest.ArgvFile), "--headless") {
		t.Fatal("a headed launch must not carry --headless")
	}
}

func TestLaunchDeletesStaleDevToolsActivePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{}, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	stalePort := ln.Addr().(*net.TCPAddr).Port
	b, profile, err := launchFake(t, nil, func(o *LaunchOptions) {
		makePrivateParent(t, o.Profile)
		if err := os.MkdirAll(o.Profile, 0o700); err != nil {
			t.Fatal(err)
		}
		stale := []byte(strings.Join([]string{strconv.Itoa(stalePort), browsertest.BrowserPath}, "\n") + "\n")
		if err := os.WriteFile(filepath.Join(o.Profile, devToolsFile), stale, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.wsURL, ":"+strconv.Itoa(stalePort)+"/") {
		t.Fatal("launched against the stale port")
	}
	if _, err := b.Page(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
		t.Fatal("the stale port was dialed")
	default:
	}
	_ = profile
}

func TestLaunchTimeout(t *testing.T) {
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvNoPort: "1"}, func(o *LaunchOptions) { o.Timeout = 300 * time.Millisecond })
	requireCode(t, err, errs.CodeBrowserFailed)
	var coded *errs.Coded
	errors.As(err, &coded)
	if !strings.Contains(coded.Message, "did not start") || !strings.Contains(coded.Fix, "--browser chrome") || coded.Exit != 1 {
		t.Fatalf("timeout error = %+v", coded)
	}
	if b != nil {
		t.Fatal("no browser on failure")
	}
	requireNoProfileProcs(t, profile)
	if _, statErr := os.Stat(pidPath(profile)); !os.IsNotExist(statErr) {
		t.Fatal("the pid file must be gone")
	}
	requireLockFree(t, profile)
}

func TestLaunchContextCancelled(t *testing.T) {
	t.Setenv(browsertest.EnvNoPort, "1")
	exe := browsertest.FakeBrowser(t)
	profile := newProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := Launch(ctx, LaunchOptions{Exe: exe, Profile: profile, Headless: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	requireNoProfileProcs(t, profile)
	requireLockFree(t, profile)
}

func TestLaunchProfileBusy(t *testing.T) {
	exe := browsertest.FakeBrowser(t)
	profile := newProfile(t)
	release, err := store.AcquireLock(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Launch(context.Background(), LaunchOptions{Exe: exe, Profile: profile, Headless: true})
	requireCode(t, err, errs.CodeBrowserBusy)
	var coded *errs.Coded
	errors.As(err, &coded)
	if coded.Exit != 4 || !strings.Contains(coded.Fix, "close the m365crawl sign-in window or wait for the other fetch") &&
		!strings.Contains(coded.Fix, "Close the m365crawl sign-in window or wait for the other fetch") {
		t.Fatalf("busy error = %+v", coded)
	}
	if _, statErr := os.Stat(filepath.Join(profile, browsertest.ArgvFile)); statErr == nil {
		t.Fatal("nothing may be started when the profile is busy")
	}
}

func TestLaunchLockErrorPassesThrough(t *testing.T) {
	old := acquireLock
	t.Cleanup(func() { acquireLock = old })
	boom := errors.New("boom")
	acquireLock = func(string) (func(), error) { return nil, boom }
	if _, err := Launch(context.Background(), LaunchOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestLaunchHandoffExit(t *testing.T) {
	old := singletonLiveFn
	t.Cleanup(func() { singletonLiveFn = old })
	calls := 0
	singletonLiveFn = func(string) bool { calls++; return calls > 1 } // free before the launch, held after
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvHandoff: "1"}, nil)
	requireCode(t, err, errs.CodeBrowserBusy)
	if b != nil {
		t.Fatal("no browser on a handoff")
	}
	requireNoProfileProcs(t, profile)
	requireLockFree(t, profile)
}

func TestLaunchEarlyExitWithoutSingletonFails(t *testing.T) {
	_, _, err := launchFake(t, map[string]string{browsertest.EnvExitCode: "3"}, nil)
	requireCode(t, err, errs.CodeBrowserFailed)
	if !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestLaunchLeaderExitsAfterWritingPort(t *testing.T) {
	old := pollEvery
	t.Cleanup(func() { pollEvery = old })
	pollEvery = time.Hour // only the exit event can end the wait
	b, profile, err := launchFake(t, map[string]string{browsertest.EnvLeaderExits: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	requireNoProfileProcs(t, profile)
}

func TestLaunchStartFails(t *testing.T) {
	profile := newProfile(t)
	_, err := Launch(context.Background(), LaunchOptions{Exe: filepath.Join(t.TempDir(), "nope"), Profile: profile})
	requireCode(t, err, errs.CodeBrowserFailed)
	requireLockFree(t, profile)
}

func TestLaunchAdoptFails(t *testing.T) {
	old := adoptProc
	t.Cleanup(func() { adoptProc = old })
	adoptProc = func(*exec.Cmd) (*group, error) { return nil, errors.New("no job") }
	_, profile, err := launchFake(t, nil, nil)
	requireCode(t, err, errs.CodeBrowserFailed)
	requireNoProfileProcs(t, profile)
	requireLockFree(t, profile)
}

func TestLaunchPidFileWriteFails(t *testing.T) {
	_, profile, err := launchFake(t, nil, func(o *LaunchOptions) {
		// A directory with a file in it where the pid file goes: it cannot be removed or written.
		makePrivateParent(t, o.Profile)
		dir := pidPath(o.Profile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	requireCode(t, err, errs.CodeBrowserFailed)
	requireNoProfileProcs(t, profile)
	requireLockFree(t, profile)
}

func TestLaunchProfileDirFails(t *testing.T) {
	old := acquireLock
	t.Cleanup(func() { acquireLock = old })
	acquireLock = func(string) (func(), error) { return func() {}, nil }
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Launch(context.Background(), LaunchOptions{Exe: "x", Profile: filepath.Join(file, "browser")})
	requireCode(t, err, errs.CodeBrowserFailed)
}

func TestReadDevToolsPort(t *testing.T) {
	cases := map[string]string{
		"9222\n/devtools/browser/abc\n": "ws://127.0.0.1:9222/devtools/browser/abc",
		"0\n/devtools/browser/abc":      "",
		"70000\n/devtools/browser/abc":  "",
		"x\n/devtools/browser/abc":      "",
		"9222\n/other":                  "",
		"9222":                          "",
		"":                              "",
	}
	for content, want := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, devToolsFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := readDevToolsPort(dir)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v, want %q", content, got, ok, want)
		}
	}
	if _, ok := readDevToolsPort(t.TempDir()); ok {
		t.Error("a missing file is not a port")
	}
}

func TestProfileDir(t *testing.T) {
	if got := ProfileDir(filepath.Join("a", "b", "archive.db")); got != filepath.Join("a", "b", "browser") {
		t.Fatalf("ProfileDir = %s", got)
	}
}

func TestEnsureProfileDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "browser")
	if err := EnsureProfileDir(p); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProfileDir(p); err != nil {
		t.Fatal(err)
	}
	checkProfileMode(t, p)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProfileDir(filepath.Join(file, "browser")); err == nil {
		t.Fatal("a profile under a file cannot be created")
	}
}

func TestLaunchStdioIsNullDevice(t *testing.T) {
	skipStdioCheckOnWindows(t)
	_, profile, err := launchFake(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFakeFile(t, profile, browsertest.StdioFile); got != "null" {
		t.Fatalf("browser stdio = %s, want the null device", got)
	}
}

func TestExitStatus(t *testing.T) {
	if exitStatus(nil) != "0" || exitStatus(errors.New("signal: killed")) != "signal: killed" {
		t.Fatal("exitStatus")
	}
}
