package workiq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// A WorkIQ child that ends after the session began is a lost session, which a caller may reopen,
// and the error says what the child left behind: its exit status and a redacted stderr tail.
func TestWorkIQChildEndingMidSessionIsALostSessionWithADiagnostic(t *testing.T) {
	useStub(t, "die")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Fetch(context.Background(), "/me")
	requireCode(t, err, "workiq_session_lost")
	d, ok := DiagnosticOf(err)
	if !ok || d.Exit != "exit status 2" || !strings.Contains(d.Stderr, "fatal: lost connection for") || !strings.Contains(d.Stderr, "subject") {
		t.Fatalf("diagnostic = %+v %v", d, ok)
	}
	for _, private := range []string{"test.user@example.invalid", "graph.invalid", "sig=abc", "Quarterly private plan", "eyJhbGci", "privatepayload"} {
		if strings.Contains(d.Stderr, private) || strings.Contains(err.Error(), private) {
			t.Fatalf("diagnostic kept %q: %+v", private, d)
		}
	}
	_ = client.Close()
}

// Non-JSON stdout is a protocol error whose diagnostic shows the redacted line, never the raw text.
func TestWorkIQProtocolErrorCarriesTheRedactedStdoutLine(t *testing.T) {
	old := handshakeTimeout
	handshakeTimeout = requestTimeout
	t.Cleanup(func() { handshakeTimeout = old })
	useStub(t, "log-noise")
	_, err := Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_protocol")
	if d, ok := DiagnosticOf(err); !ok || !strings.Contains(d.Stdout, "Starting MCP server") || d.Stderr != "" {
		t.Fatalf("diagnostic = %+v %v", d, ok)
	}
	if _, ok := DiagnosticOf(errors.New("plain")); ok {
		t.Fatal("an error without a child diagnostic reported one")
	}
}

func TestWorkIQDiagnosticRedactionIsBounded(t *testing.T) {
	long := strings.Repeat("word ", 400) + "tail-end"
	got := redactDiagnostic([]byte("bad \xff\x01 "+long), 64)
	if len(got) > 64 || !strings.HasSuffix(got, "tail-end") || strings.Contains(got, "\x01") || !utf8.ValidString(got) {
		t.Fatalf("redacted tail = %q", got)
	}
	if redactDiagnostic(nil, 64) != "" || redactDiagnostic([]byte("  \n "), 64) != "" {
		t.Fatal("empty output produced a diagnostic")
	}
}
