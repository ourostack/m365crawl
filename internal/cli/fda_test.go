package cli

import (
	"fmt"
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
