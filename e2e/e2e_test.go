//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
