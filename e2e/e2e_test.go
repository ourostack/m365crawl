//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "teamscrawl-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir temp:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binary = filepath.Join(dir, "teamscrawl")
	build := exec.Command( //nolint:gosec // fixed arguments; binary path is a temp dir we created
		"go", "build", "-ldflags", "-X github.com/ourostack/teamscrawl/internal/cli.version=e2e", "-o", binary, "../cmd/teamscrawl")
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, "version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("teamscrawl version: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "e2e" {
		t.Fatalf("version output = %q, want %q", got, "e2e")
	}
}

// SIGTERM while watch is mid-sync (held after the snapshot) exits 0 and removes the snapshot.
func TestWatchSIGTERM(t *testing.T) {
	tmp := t.TempDir()
	root, err := filepath.Abs("../testdata/teams-fixture/EBWebView")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, "watch", "--every", "1h", "--db", filepath.Join(tmp, "a.db"), "--teams-root", root, "--json") //nolint:gosec // G204: binary is the one this test built
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT=30s")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(tmp, "teamscrawl-snapshot-*")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m, _ := filepath.Glob(snap); len(m) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("no snapshot appeared\nstderr: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("watch after SIGTERM: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if m, _ := filepath.Glob(snap); len(m) != 0 {
		t.Fatalf("snapshot left behind: %v", m)
	}
}
