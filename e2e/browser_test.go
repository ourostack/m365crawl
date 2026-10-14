//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
)

// requireBrowserEnv makes a missing browser a failure. The CI jobs on macOS and Windows set it:
// those runners have Edge, and a skipped run would prove nothing about leftover processes.
const requireBrowserEnv = "M365CRAWL_REQUIRE_BROWSER"

// profileProcesses lists only processes whose command line mentions this fixture's profile.
func profileProcesses(t *testing.T, profile string) []string {
	t.Helper()
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		script := `$p = $env:M365CRAWL_PROFILE; ` +
			`Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -and $_.CommandLine.Contains($p) } | ForEach-Object { "$($_.ProcessId) $($_.CommandLine)" }`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Env = append(os.Environ(), "M365CRAWL_PROFILE="+profile)
		out, err = cmd.Output()
	} else {
		out, err = exec.Command("ps", "-axww", "-o", "pid=,args=").Output()
	}
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	var processes []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, profile) && !strings.HasPrefix(strings.TrimSpace(line), strconv.Itoa(os.Getpid())+" ") {
			processes = append(processes, line)
		}
	}
	return processes
}

func profileProcessCount(t *testing.T, profile string) int {
	t.Helper()
	return len(profileProcesses(t, profile))
}

func requireDisposableFixture(t *testing.T, options browser.LaunchOptions) {
	t.Helper()
	if !options.Headless || !options.DisposableFixture {
		t.Fatal("synthetic browser must explicitly opt in to headless disposable fixture policy before Launch")
	}
}

func requireFixtureBrowserProcess(t *testing.T, profile string) {
	t.Helper()
	for _, line := range profileProcesses(t, profile) {
		if strings.Contains(line, "--type=") {
			continue
		}
		flags := strings.Fields(line)
		if !slices.Contains(flags, "--use-mock-keychain") || !slices.Contains(flags, "--headless=new") {
			t.Fatalf("fixture browser lacks explicit live mock-keychain/headless flags: %s", line)
		}
		t.Logf("live disposable fixture browser: %s", strings.TrimSpace(line))
		return
	}
	t.Fatal("no live fixture browser leader; flag check is blind")
}

func requireProfileClosed(t *testing.T, profile string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for profileProcessCount(t, profile) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d processes started with the profile are still running after Close", profileProcessCount(t, profile))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("fixture profile processes closed: %s", profile)
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
	// The archive directory is created by Launch, with the private access rules Windows demands.
	profile := filepath.Join(t.TempDir(), "archive", "browser")
	t.Cleanup(func() { _ = browser.SweepOrphan(profile) }) // a failed assertion must not leak a browser

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	options := browser.LaunchOptions{Exe: exe, Kind: kind, Profile: profile, Headless: true, DisposableFixture: true}
	requireDisposableFixture(t, options)
	b, err := browser.Launch(ctx, options)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = b.Close()
		}
	}()
	requireFixtureBrowserProcess(t, profile)
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
	requireProfileClosed(t, profile)
}
