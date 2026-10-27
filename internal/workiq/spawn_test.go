package workiq

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkIQAddressAndProcessRefusals(t *testing.T) {
	useStub(t, "")
	for _, address := range []string{
		"x", strings.Repeat("x", 255), strings.Repeat("x", 65) + "@example.invalid",
		"x@", "x@a..b", "x@" + strings.Repeat("x", 64), "x@a_underscore.invalid",
	} {
		_, err := Open(context.Background(), Invocation{}, address)
		requireCode(t, err, "workiq_account_invalid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, Invocation{}, "")
	requireCode(t, err, "workiq_timeout")
	_, err = startWorkIQ(context.Background(), Invocation{}, mode(99), "")
	requireCode(t, err, "workiq_protocol")
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent"))
	_, err = Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_not_installed")
	_, err = Version(context.Background(), Invocation{})
	requireCode(t, err, "workiq_not_installed")
}

func TestWorkIQBinaryLookupNeverBypassesTestGuard(t *testing.T) {
	t.Setenv("M365CRAWL_WORKIQ_BIN", "")
	t.Setenv("PATH", t.TempDir())
	_, err := chooseBinary(Invocation{}, false)
	requireCode(t, err, "workiq_not_installed")
	for _, inv := range []Invocation{{ExplicitTeamsRoot: true}, {ExplicitOutlookRoot: true}} {
		_, err := chooseBinary(inv, false)
		requireCode(t, err, "workiq_hermetic_required")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "workiq"
	if strings.HasSuffix(strings.ToLower(exe), ".exe") {
		name += ".exe"
	}
	path := filepath.Join(os.Getenv("PATH"), name)
	// Copy only this synthetic helper binary; no existing WorkIQ installation is opened.
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := chooseBinary(Invocation{}, false); err != nil || got != path {
		t.Fatalf("owned PATH helper lookup: %q %v", got, err)
	}
	_, err = Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_hermetic_required")
}

func TestWorkIQPipeAndAdoptionFailuresAreSanitized(t *testing.T) {
	useStub(t, "")
	originalBuild, originalAdopt := buildCommand, adoptChild
	t.Cleanup(func() { buildCommand, adoptChild = originalBuild, originalAdopt })
	for _, stage := range []string{"command", "input", "output", "adopt"} {
		t.Run(stage, func(t *testing.T) {
			buildCommand = func(path string, args []string) (*exec.Cmd, error) {
				if stage == "command" {
					return nil, failure("workiq_binary_unsafe")
				}
				cmd, err := originalBuild(path, args)
				if stage == "input" {
					cmd.Stdin = strings.NewReader("")
				}
				if stage == "output" {
					cmd.Stdout = io.Discard
				}
				return cmd, err
			}
			adoptChild = func(cmd *exec.Cmd) (*processGroup, error) {
				if stage == "adopt" {
					return nil, errors.New("private-download")
				}
				return originalAdopt(cmd)
			}
			_, err := Open(context.Background(), Invocation{}, "")
			code := "workiq_failed"
			if stage == "command" {
				code = "workiq_binary_unsafe"
			} else if stage == "adopt" {
				code = "workiq_cleanup_failed"
			}
			requireCode(t, err, code)
		})
	}
}

func TestWorkIQVersionFailuresDoNotPrintChildOutput(t *testing.T) {
	for mode, code := range map[string]string{
		"version-noisy": "workiq_protocol", "version-eula": "workiq_eula_required",
		"version-unknown": "workiq_failed", "hang": "workiq_timeout",
	} {
		t.Run(mode, func(t *testing.T) {
			useStub(t, mode)
			old := requestTimeout
			if mode == "hang" {
				requestTimeout = 100 * time.Millisecond
			}
			defer func() { requestTimeout = old }()
			got, err := Version(context.Background(), Invocation{})
			requireCode(t, err, code)
			if got != "" {
				t.Fatal("untrusted version stdout exposed")
			}
		})
	}
	useStub(t, "")
	old := readVersion
	readVersion = func(io.Reader) ([]byte, error) { return nil, io.ErrUnexpectedEOF }
	defer func() { readVersion = old }()
	_, err := Version(context.Background(), Invocation{})
	requireCode(t, err, "workiq_protocol")
}
