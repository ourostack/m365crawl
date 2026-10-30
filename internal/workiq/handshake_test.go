package workiq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestWorkIQVersionAcceptsTheReleasedForms(t *testing.T) {
	for _, mode := range []string{"", "version-real", "version-build"} {
		useStub(t, mode)
		if v, err := Version(context.Background(), Invocation{}); err != nil || v != "1.0.1" {
			t.Fatalf("%s: version %q %v", mode, v, err)
		}
	}
}

func shortHandshake(t *testing.T) {
	t.Helper()
	old := handshakeTimeout
	handshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = old })
}

func TestWorkIQSilentHandshakeIsASignInRequirement(t *testing.T) {
	shortHandshake(t)
	useStub(t, "signed-out")
	started := time.Now()
	_, err := Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_signin_required")
	var coded *errs.Coded
	if !errors.As(err, &coded) || !strings.Contains(coded.Fix, "workiq auth login") || !strings.Contains(coded.Fix, "never signs in") {
		t.Fatalf("fix = %q", coded.Fix)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatal("a silent handshake was not bounded by the handshake deadline and cleanup", elapsed)
	}
	// Output before the deadline means WorkIQ is running, not waiting for a sign-in.
	useStub(t, "signed-out-stderr")
	_, err = Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_timeout")
	// A caller's own cancellation is not a sign-in requirement.
	useStub(t, "signed-out")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = Open(ctx, Invocation{}, "")
	requireCode(t, err, "workiq_timeout")
}

func TestWorkIQLogsOnStdoutAreAClearProtocolError(t *testing.T) {
	shortHandshake(t)
	useStub(t, "log-noise")
	_, err := Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_protocol")
	var coded *errs.Coded
	if !errors.As(err, &coded) || !strings.Contains(coded.Fix, "log") || strings.Contains(err.Error(), "Starting MCP server") {
		t.Fatalf("protocol error did not explain stdout logs or echoed them: %v %q", err, coded.Fix)
	}
}
