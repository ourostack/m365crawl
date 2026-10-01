package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResponsibleAppWalksToFirstBundle(t *testing.T) {
	tree := map[int]struct {
		ppid int
		cmd  string
	}{
		500: {400, "/bin/zsh"},
		400: {300, "/Applications/Ghostty.app/Contents/MacOS/ghostty"},
		300: {1, "/sbin/launchd"},
	}
	lookup := func(pid int) (int, string, error) {
		n, ok := tree[pid]
		if !ok {
			return 0, "", fmt.Errorf("no pid %d", pid)
		}
		return n.ppid, n.cmd, nil
	}
	if got := responsibleApp(lookup, 500, "Apple_Terminal"); got != "Ghostty" {
		t.Fatalf("got %q", got)
	}
}

func TestResponsibleAppFallsBackToTermProgram(t *testing.T) {
	lookup := func(int) (int, string, error) { return 1, "/bin/zsh", nil }
	if got := responsibleApp(lookup, 10, "Apple_Terminal"); got != "Terminal" {
		t.Fatalf("got %q", got)
	}
	if got := responsibleApp(lookup, 10, "vscode"); got != "Visual Studio Code" {
		t.Fatalf("got %q", got)
	}
	if got := responsibleApp(lookup, 10, "WezTerm"); got != "WezTerm" {
		t.Fatalf("got %q", got)
	}
	if got := responsibleApp(lookup, 10, ""); !strings.Contains(got, "terminal") {
		t.Fatalf("generic fallback: %q", got)
	}
}

func TestResponsibleAppStopsOnCycle(t *testing.T) {
	lookup := func(pid int) (int, string, error) { return pid, "/bin/zsh", nil }
	done := make(chan string)
	go func() { done <- responsibleApp(lookup, 7, "") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cycle in process chain hangs")
	}
}

func TestResponsibleAppDeepBundlePath(t *testing.T) {
	lookup := func(pid int) (int, string, error) {
		return 1, "/Applications/Some Tool.app/Contents/Frameworks/Helper.app/Contents/MacOS/helper --flag x", nil
	}
	if got := responsibleApp(lookup, 9, ""); got != "Some Tool" {
		t.Fatalf("outermost bundle expected, got %q", got)
	}
}

func stubPS(t *testing.T, fn func(pid int) ([]byte, error)) {
	t.Helper()
	old := runPS
	runPS = fn
	t.Cleanup(func() { runPS = old })
}

func TestPsLookupParsesParentAndCommand(t *testing.T) {
	stubPS(t, func(pid int) ([]byte, error) {
		return []byte("  321 /Applications/Foo.app/Contents/MacOS/foo --bar\n"), nil
	})
	ppid, cmd, err := psLookup(9)
	if err != nil || ppid != 321 || cmd != "/Applications/Foo.app/Contents/MacOS/foo --bar" {
		t.Fatalf("psLookup = %d, %q, %v", ppid, cmd, err)
	}
}

func TestPsLookupReportsEveryFailureMode(t *testing.T) {
	cases := map[string]struct {
		out []byte
		err error
	}{
		"ps fails":          {nil, fmt.Errorf("exit 1")},
		"no such process":   {[]byte(""), nil},
		"one field only":    {[]byte("12\n"), nil},
		"ppid not a number": {[]byte("abc /bin/zsh\n"), nil},
	}
	for name, c := range cases {
		stubPS(t, func(int) ([]byte, error) { return c.out, c.err })
		if _, _, err := psLookup(9); err == nil {
			t.Errorf("%s: psLookup succeeded", name)
		}
	}
}

func TestRealPsFindsThisProcess(t *testing.T) {
	ppid, cmd, err := psLookup(os.Getpid())
	if err != nil {
		t.Skipf("ps is not usable here: %v", err)
	}
	if ppid != os.Getppid() || cmd == "" {
		t.Fatalf("psLookup(self) = %d, %q; want parent %d and a command", ppid, cmd, os.Getppid())
	}
}

func TestFdaFixNamesTheResponsibleApp(t *testing.T) {
	stubPS(t, func(int) ([]byte, error) { return nil, fmt.Errorf("no ps") })
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := fdaFix(); !strings.Contains(got, "turn it on for Ghostty") {
		t.Fatalf("fdaFix = %q", got)
	}
}
