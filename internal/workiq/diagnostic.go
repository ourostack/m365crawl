package workiq

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Diagnostic is what m365crawl may report about a WorkIQ child that failed: its exit status, the
// redacted end of its stderr and the redacted start of a non-JSON stdout line. Credentials,
// addresses, URLs, quoted text and long opaque tokens are replaced, so it carries no message
// content or secret, only the shape of what went wrong.
type Diagnostic struct {
	Exit   string `json:"exit,omitempty"`
	Stderr string `json:"stderr_tail,omitempty"`
	Stdout string `json:"stdout,omitempty"`
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

var (
	diagnosticURL     = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	diagnosticAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+`)
	diagnosticQuoted  = regexp.MustCompile(`"[^"]*"|'[^']*'|“[^”]*”`)
	diagnosticToken   = regexp.MustCompile(`[A-Za-z0-9+/=_.\-]{24,}`)
	diagnosticSpace   = regexp.MustCompile(`\s+`)
)

// redactDiagnostic keeps at most limit bytes from the end of raw after redaction.
func redactDiagnostic(raw []byte, limit int) string {
	text := strings.ToValidUTF8(string(raw), " ")
	encoded, _ := json.Marshal(text)
	safe, _ := teamsdesktop.Scrub(encoded)
	_ = json.Unmarshal(safe, &text) // Scrub keeps a JSON string a JSON string
	text = diagnosticURL.ReplaceAllString(text, "[url]")
	text = diagnosticAddress.ReplaceAllString(text, "[address]")
	text = diagnosticQuoted.ReplaceAllString(text, "[text]")
	text = diagnosticToken.ReplaceAllString(text, "[id]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(diagnosticSpace.ReplaceAllString(text, " "))
	if len(text) > limit {
		text = strings.TrimSpace(strings.ToValidUTF8(text[len(text)-limit:], ""))
	}
	return text
}

// diagnose attaches what the child left behind to a coded failure, when it left anything.
func (c *Client) diagnose(err *errs.Coded) error {
	d := Diagnostic{Exit: c.p.errors.exitStatus(), Stderr: redactDiagnostic(c.p.errors.tailBytes(), diagnosticStderr),
		Stdout: redactDiagnostic(c.junk, diagnosticStdout)}
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
