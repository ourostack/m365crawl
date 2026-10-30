package workiq

import (
	"context"
	"errors"
	"regexp"
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
	if !ok || d.Exit != "exit status 2" || !strings.Contains(d.Stderr, "fatal: lost connection for [redacted]") {
		t.Fatalf("diagnostic = %+v %v", d, ok)
	}
	for _, private := range []string{"test.user", "example.invalid", "graph.invalid", "sig=abc", "Quarterly", "private", "plan", "eyJhbGci", "payload"} {
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
	if d, ok := DiagnosticOf(err); !ok || d.Stdout != "info: [redacted] Starting MCP server" || d.Stderr != "" {
		t.Fatalf("diagnostic = %+v %v", d, ok)
	}
	if _, ok := DiagnosticOf(errors.New("plain")); ok {
		t.Fatal("an error without a child diagnostic reported one")
	}
}

var (
	diagnosticLetters = regexp.MustCompile(`[\p{L}]+`)
	diagnosticHash    = regexp.MustCompile(`id#[0-9a-f]{8}`)
)

// requireAllowlisted fails unless every word left in a diagnostic is a known-safe word, an error
// class name, or the redaction and hash markers, whatever the cut that produced it.
func requireAllowlisted(t *testing.T, where string, got string) {
	t.Helper()
	if !utf8.ValidString(got) {
		t.Fatalf("%s: invalid UTF-8 %q", where, got)
	}
	for _, word := range diagnosticLetters.FindAllString(diagnosticHash.ReplaceAllString(got, "id"), -1) {
		if word != "redacted" && word != "id" && !diagnosticVocabulary[strings.ToLower(word)] && !diagnosticClassPart(got, word) {
			t.Fatalf("%s: kept %q in %q", where, word, got)
		}
	}
}

// diagnosticClassPart says word belongs to an exception or error class name the diagnostic kept.
func diagnosticClassPart(text, word string) bool {
	for _, class := range diagnosticClass.FindAllString(text, -1) {
		if strings.Contains(class, word) {
			return true
		}
	}
	return false
}

const privateLog = `fatal: send failed subject="Confidential: Q4 layoffs for the platform team" token eyJhbGciOiJIUzI1NiJ9xyz` + "\n" +
	`warn: "body": "We will announce the acquisition of Fabrikam on Monday", preview=Hello, name=Ann, from=ann@contoso.example, to=bob@contoso.example, email=c@d.example, content=Secret` + "\n" +
	`error: échec connexion perdue für Größe 東京 at https://graph.example/v1?sig=abc` + "\n" +
	`System.Net.Http.HttpRequestException: connection reset; request-id 6f1c2a4e-1111-2222-3333-444455556666 status 503`

// Cutting before redaction cannot keep content: every cut point of stderr's tail and stdout's head
// leaves only allowlisted words.
func TestWorkIQDiagnosticRedactionSurvivesEveryCut(t *testing.T) {
	for cut := 0; cut <= len(privateLog); cut++ {
		requireAllowlisted(t, "stderr tail", diagnosticTail([]byte(privateLog[cut:]), cut > 0, diagnosticStderr))
		requireAllowlisted(t, "stdout head", diagnosticHead([]byte(privateLog[:cut]), cut < len(privateLog), diagnosticStdout))
	}
	full := diagnosticTail([]byte(privateLog), false, 4096)
	for _, want := range []string{"fatal: send failed [redacted]", "System.Net.Http.HttpRequestException: connection reset", "status 503", "id#"} {
		if !strings.Contains(full, want) {
			t.Fatalf("redaction lost the diagnostic's safe shape %q: %q", want, full)
		}
	}
	if strings.Contains(full, "6f1c2a4e") || strings.Contains(full, "for the platform") {
		t.Fatalf("request id or content survived: %q", full)
	}
}

func TestWorkIQDiagnosticBoundsAreLineAligned(t *testing.T) {
	lines := strings.Repeat("error: connection reset\n", 60) + "fatal: lost connection"
	got := diagnosticTail([]byte(lines), true, 64)
	if len(got) > 64 || !strings.HasSuffix(got, "fatal: lost connection") || strings.HasPrefix(got, "reset") {
		t.Fatalf("tail = %q", got)
	}
	long := diagnosticTail([]byte(strings.Repeat("error ", 100)), false, 30)
	if len(long) > 30 || strings.HasSuffix(long, "erro") {
		t.Fatalf("long line tail = %q", long)
	}
	head := diagnosticHead([]byte("info: starting server status 200 and then more words"), true, 24)
	if len(head) > 24 || !strings.HasPrefix(head, "info: starting server") {
		t.Fatalf("head = %q", head)
	}
	if diagnosticTail(nil, false, 64) != "" || diagnosticTail([]byte("partial line only"), true, 64) != "" || diagnosticHead([]byte("  \n "), false, 64) != "" {
		t.Fatal("empty or wholly partial output produced a diagnostic")
	}
}
