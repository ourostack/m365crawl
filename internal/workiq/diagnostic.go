package workiq

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Diagnostic is what m365crawl may report about a WorkIQ child that failed: its exit status, the
// allowlisted end of its stderr, the allowlisted start of a non-JSON stdout line, and a failed
// server or tool result's descriptor. Only common
// log and transport words, error class names, HTTP status numbers and hashed request IDs survive,
// so it carries no message content or secret, only the shape of what went wrong.
type Diagnostic struct {
	Exit   string `json:"exit,omitempty"`
	Stderr string `json:"stderr_tail,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	// Server describes a failed server or tool result: its HTTP status, a Graph error code that is a
	// plain identifier, and the allowlisted shape of its text.
	Server string `json:"server,omitempty"`
}

const (
	diagnosticStderr = 512
	diagnosticStdout = 256
)

// diagnosed is a coded WorkIQ failure with the child's diagnostic; it unwraps to the code.
type diagnosed struct {
	err *errs.Coded
	d   Diagnostic
}

func (e *diagnosed) Error() string { return e.err.Error() }
func (e *diagnosed) Unwrap() error { return e.err }

// DiagnosticOf returns the WorkIQ child diagnostic an error carries.
func DiagnosticOf(err error) (Diagnostic, bool) {
	var d *diagnosed
	if errors.As(err, &d) {
		return d.d, true
	}
	return Diagnostic{}, false
}

// The diagnostic is an allowlist: each line keeps only common log and transport words, exception
// or error class names, HTTP status numbers and request IDs reduced to a short hash. Every other
// word, quoted or not, whole or cut, in any script, becomes [redacted]. Cutting the output to its
// bounds therefore cannot expose content; cuts fall on line and word boundaries for readability.
var diagnosticVocabulary = map[string]bool{
	"a": true, "abort": true, "aborted": true, "access": true, "after": true, "an": true, "at": true,
	"auth": true, "authentication": true, "authorization": true, "bad": true, "be": true, "been": true,
	"before": true, "broken": true, "by": true, "call": true, "can": true, "cancel": true, "canceled": true,
	"cancelled": true, "cannot": true, "certificate": true, "client": true, "close": true, "closed": true,
	"closing": true, "code": true, "connect": true, "connected": true, "connecting": true, "connection": true,
	"connections": true, "consent": true, "could": true, "crash": true, "crashed": true, "debug": true,
	"denied": true, "disconnected": true, "disk": true, "dns": true, "down": true, "during": true, "end": true,
	"eof": true, "error": true, "errors": true, "eula": true, "exception": true, "exit": true, "exited": true,
	"exiting": true, "expired": true, "fail": true, "failed": true, "failure": true, "failures": true,
	"fatal": true, "fetch": true, "fetching": true, "for": true, "forbidden": true, "found": true, "from": true,
	"full": true, "gateway": true, "graph": true, "handshake": true, "http": true, "in": true,
	"info": true, "internal": true, "invalid": true, "is": true, "kill": true, "killed": true, "limit": true,
	"limited": true, "login": true, "loss": true, "lost": true, "mcp": true, "memory": true, "missing": true,
	"network": true, "no": true, "not": true, "of": true, "on": true, "out": true, "pipe": true, "policy": true,
	"process": true, "rate": true, "read": true, "receive": true, "receiving": true, "refused": true,
	"request": true, "requests": true, "required": true, "reset": true, "response": true, "responses": true,
	"retries": true, "retry": true, "retrying": true, "send": true, "sending": true, "server": true,
	"service": true, "session": true, "shutdown": true, "shutting": true, "sign": true, "signal": true,
	"signin": true, "socket": true, "ssl": true, "started": true, "starting": true, "status": true,
	"stderr": true, "stdin": true, "stdio": true, "stdout": true, "stopped": true, "stopping": true,
	"stream": true, "terminate": true, "terminated": true, "the": true, "throttled": true, "throttling": true,
	"time": true, "timed": true, "timeout": true, "timeouts": true, "tls": true, "to": true, "token": true,
	"tool": true, "trace": true, "transport": true, "unable": true, "unauthorized": true, "unavailable": true,
	"unexpected": true, "unexpectedly": true, "unhandled": true, "unreachable": true, "version": true,
	"warn": true, "warning": true, "was": true, "while": true, "with": true, "workiq": true, "write": true,
}

var (
	diagnosticWord    = regexp.MustCompile(`[\p{L}\p{N}_.\-@+/%~]+`)
	diagnosticClass   = regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9]*\.)*[A-Z][A-Za-z0-9]*(?:Exception|Error)\b`)
	diagnosticGUID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	diagnosticStatus  = regexp.MustCompile(`^[1-5][0-9][0-9]$`)
	diagnosticRedacts = regexp.MustCompile(`\[redacted\](?:[\s=:,;/#()-]*\[redacted\])+`)
	diagnosticSpace   = regexp.MustCompile(`\s+`)
)

func diagnosticSafeWord(word string) string {
	switch {
	case diagnosticVocabulary[strings.ToLower(strings.TrimRight(word, ".-"))], diagnosticStatus.MatchString(word),
		diagnosticClass.FindString(word) == word:
		return word
	case diagnosticGUID.MatchString(word):
		sum := sha256.Sum256([]byte(strings.ToLower(word)))
		return "id#" + hex.EncodeToString(sum[:4])
	}
	return "[redacted]"
}

// redactDiagnosticLine keeps the allowlisted shape of one output line.
func redactDiagnosticLine(line string) string {
	line = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_.-@+/%~:;,()=#", r) {
			return r
		}
		return ' ' // quotes, brackets, controls and other symbols only separate words
	}, line)
	line = diagnosticWord.ReplaceAllStringFunc(line, diagnosticSafeWord)
	line = diagnosticRedacts.ReplaceAllString(line, "[redacted]")
	return strings.TrimSpace(diagnosticSpace.ReplaceAllString(line, " "))
}

func diagnosticLines(raw []byte, partialFirst bool) []string {
	lines := strings.Split(strings.ToValidUTF8(string(raw), " "), "\n")
	if partialFirst {
		lines = lines[1:]
	}
	var out []string
	for _, line := range lines {
		if line = redactDiagnosticLine(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// diagnosticTail is the redacted end of stderr in at most limit bytes, whole lines first; cut says
// the buffer lost its start, so its first line is partial and dropped.
func diagnosticTail(raw []byte, cut bool, limit int) string {
	lines := diagnosticLines(raw, cut)
	text := ""
	for i := len(lines) - 1; i >= 0; i-- {
		next := lines[i]
		if text != "" {
			next += "; " + text
		}
		if len(next) <= limit {
			text = next
			continue
		}
		if text == "" { // one line longer than the bound: keep its end, from a word start
			text = next[len(next)-limit:]
			if space := strings.IndexByte(text, ' '); space >= 0 {
				text = text[space+1:]
			}
		}
		break
	}
	return text
}

// diagnosticHead is the redacted start of one non-JSON stdout line in at most limit bytes; cut
// says the line was longer than what was kept, so its partial last word is dropped.
func diagnosticHead(raw []byte, cut bool, limit int) string {
	text := ""
	if lines := diagnosticLines(raw, false); len(lines) > 0 {
		text = lines[0]
	}
	if cut {
		text = text[:max(0, strings.LastIndexByte(text, ' '))]
	}
	if len(text) > limit {
		text = text[:max(0, strings.LastIndexByte(text[:limit+1], ' '))] // never end inside a word
	}
	return strings.TrimSpace(text)
}

// diagnose attaches what the child left behind to a coded failure, when it left anything.
func (c *Client) diagnose(err *errs.Coded) error {
	tail, cut := c.p.errors.tailBytes()
	d := Diagnostic{Exit: c.p.errors.exitStatus(), Stderr: diagnosticTail(tail, cut, diagnosticStderr),
		Stdout: diagnosticHead(c.junk, c.junkCut, diagnosticStdout)}
	if d == (Diagnostic{}) {
		return err
	}
	return &diagnosed{err: err, d: d}
}

// lost reports a child that ended after the session began, for no reason its stderr names, as a
// lost session a caller may reopen; before the handshake it stays the classified failure.
func (c *Client) lost(err *errs.Coded) *errs.Coded {
	if c.ready.Load() && err.Code == "workiq_failed" {
		return failure("workiq_session_lost")
	}
	return err
}
