//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
)

// requireBrowserEnv makes a missing browser a failure. The CI jobs on macOS and Windows set it:
// those runners have Edge, and a skipped run would prove nothing about leftover processes.
const requireBrowserEnv = "M365CRAWL_REQUIRE_BROWSER"

// profileProcessCount counts the processes whose command line mentions the profile directory.
func profileProcessCount(t *testing.T, profile string) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		script := `$p = $env:M365CRAWL_PROFILE; ` +
			`@(Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -and $_.CommandLine.Contains($p) }).Count`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Env = append(os.Environ(), "M365CRAWL_PROFILE="+profile)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("listing processes: %v", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatalf("process count %q: %v", out, err)
		}
		return n
	}
	out, err := exec.Command("ps", "-axww", "-o", "pid=,args=").Output()
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, profile) && !strings.HasPrefix(strings.TrimSpace(line), strconv.Itoa(os.Getpid())+" ") {
			n++
		}
	}
	return n
}

// TestRealBrowserLeavesNoProcess launches the real browser headless on a temporary profile,
// opens about:blank, closes it, and requires that no process started with the profile remains.
// It touches no network and never opens a window.
func TestRealBrowserLeavesNoProcess(t *testing.T) {
	exe, kind, err := browser.Find("")
	if err != nil {
		if os.Getenv(requireBrowserEnv) == "1" {
			t.Fatalf("no browser found, and %s=1 says this runner must have one: %v", requireBrowserEnv, err)
		}
		t.Skipf("no browser installed: %v", err)
	}
	profile := filepath.Join(t.TempDir(), "browser")
	t.Cleanup(func() { _ = browser.SweepOrphan(profile) }) // a failed assertion must not leak a browser

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := browser.Launch(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: profile, Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = b.Close()
		}
	}()
	page, err := b.Page(ctx)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if err := page.Navigate(ctx, "about:blank"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	var href string
	if err := page.Eval(ctx, "location.href", &href); err != nil || href != "about:blank" {
		t.Fatalf("Eval location.href = %q, %v", href, err)
	}
	if profileProcessCount(t, profile) == 0 {
		t.Fatal("the browser is not running with the profile before Close; the process check is blind")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	deadline := time.Now().Add(5 * time.Second)
	for profileProcessCount(t, profile) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d processes started with the profile are still running after Close", profileProcessCount(t, profile))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
