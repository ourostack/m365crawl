package browsertest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Environment switches for the fake browser. The test sets them with t.Setenv before Launch.
const (
	envFake        = "M365CRAWL_FAKE_BROWSER"
	envRole        = "M365CRAWL_FAKE_ROLE"
	EnvIgnoreClose = "FAKE_IGNORE_CLOSE"        // keep running after Browser.close
	EnvIgnoreTerm  = "FAKE_IGNORE_TERM"         // ignore SIGTERM (Unix)
	EnvHandoff     = "FAKE_HANDOFF"             // exit 0 at once, like a launch handed to a running instance
	EnvLateHandoff = "FAKE_LATE_HANDOFF"        // start a successor that writes the port later, then exit 0 at once
	envAfterPid    = "M365CRAWL_FAKE_AFTER_PID" // write the port only once this process has exited
	EnvExitCode    = "FAKE_EXIT_CODE"           // exit with this status at once
	EnvNoPort      = "FAKE_NO_PORT"             // never write DevToolsActivePort
	EnvLeaderExits = "FAKE_LEADER_EXITS"        // exit the leader once ready, leaving its children
	EnvEscape      = "FAKE_ESCAPE_CHILD"        // start a child in its own session (Unix)
	EnvHangEval    = "FAKE_HANG_EVAL"           // never answer Runtime.evaluate, like a hung page
)

// Files the fake writes into the profile directory.
const (
	ArgvFile  = "fake-argv.txt"
	PidsFile  = "fake.pids"
	StdioFile = "fake-stdio.txt"
)

// FakeBrowser returns the path of an executable that behaves like a headless browser: run with
// --user-data-dir it writes DevToolsActivePort, serves the fake CDP server, starts one child
// process in its process group, and exits on Browser.close unless FAKE_IGNORE_CLOSE=1. The
// executable is the test binary itself, so the package's TestMain must call RunIfFake first.
func FakeBrowser(t testing.TB) string {
	t.Helper()
	exe, _ := os.Executable()
	t.Setenv(envFake, "1")
	return exe
}

// RunIfFake runs the fake browser (or one of its children) and exits, when the process was
// started as one. Call it first in TestMain.
func RunIfFake() {
	if os.Getenv(envFake) != "1" {
		return
	}
	if os.Getenv(envRole) != "" {
		if os.Getenv(EnvIgnoreTerm) == "1" {
			ignoreTerm()
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(runFake(os.Args[1:]))
}

func profileArg(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			return v
		}
	}
	return ""
}

func runFake(args []string) int {
	profile := profileArg(args)
	if profile == "" {
		return 64
	}
	_ = os.WriteFile(filepath.Join(profile, ArgvFile), []byte(strings.Join(args, "\n")), 0o600) //nolint:gosec // G703: the profile is the test's own temp dir
	_ = os.WriteFile(filepath.Join(profile, StdioFile), []byte(stdioReport()), 0o600)           //nolint:gosec // G703: the profile is the test's own temp dir
	if os.Getenv(EnvHandoff) == "1" {
		return 0
	}
	if os.Getenv(EnvLateHandoff) == "1" {
		// Like Edge's launcher on Windows: the first process starts the one that runs the browser
		// and exits 0 before that one publishes its debugging port.
		startSuccessor(args)
		return 0
	}
	if v := os.Getenv(EnvExitCode); v != "" {
		var n int
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	if os.Getenv(EnvIgnoreTerm) == "1" {
		ignoreTerm()
	}
	pids := fmt.Sprintf("leader %d\n", os.Getpid())
	child := startChild(args, false)
	pids += fmt.Sprintf("child %d\n", child.Process.Pid)
	if os.Getenv(EnvEscape) == "1" {
		esc := startChild(args, true)
		pids += fmt.Sprintf("escaped %d\n", esc.Process.Pid)
	}
	_ = os.WriteFile(filepath.Join(profile, PidsFile), []byte(pids), 0o600) //nolint:gosec // G703: the profile is the test's own temp dir
	if os.Getenv(EnvNoPort) == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	srv := Listen()
	if os.Getenv(EnvHangEval) == "1" {
		srv.Handle("Runtime.evaluate", func(Request) (any, *Error) {
			for {
				time.Sleep(time.Hour)
			}
		})
	}
	exit := make(chan struct{})
	srv.OnClose(func() {
		if os.Getenv(EnvIgnoreClose) == "1" {
			return
		}
		_ = child.Process.Kill()
		close(exit)
	})
	if pid, err := strconv.Atoi(os.Getenv(envAfterPid)); err == nil {
		waitGone(pid)
		time.Sleep(200 * time.Millisecond) // and give the launcher time to see it exit
	}
	port := fmt.Sprintf("%d\n%s\n", srv.Port, BrowserPath)
	if err := os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), []byte(port), 0o600); err != nil { //nolint:gosec // G703: the profile is the test's own temp dir
		return 66
	}
	if os.Getenv(EnvLeaderExits) == "1" {
		return 0
	}
	<-exit
	return 0
}

// startSuccessor re-executes the test binary as the fake browser itself, with the same arguments,
// writing its port only after this process has exited. Its environment turns the handoff off, so
// it runs as the browser instead of handing off again.
func startSuccessor(args []string) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, args...) //nolint:gosec // G204: re-executing the test binary
	cmd.Env = append(os.Environ(), EnvLateHandoff+"=", envAfterPid+"="+strconv.Itoa(os.Getpid()))
	_ = cmd.Start()
}

// startChild re-executes the test binary as a helper process that only sleeps. Its argv carries
// the same --user-data-dir flag, like a real browser's helper processes.
func startChild(args []string, escape bool) *exec.Cmd {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, append([]string{"--type=fake-helper"}, args...)...) //nolint:gosec // G204: re-executing the test binary
	cmd.Env = append(os.Environ(), envRole+"=helper")
	if escape {
		detach(cmd)
	}
	_ = cmd.Start()
	return cmd
}
