package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestVersionCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "dev") {
		t.Fatalf("stdout = %q, want it to contain %q", out.String(), "dev")
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestParserConstructionFailureIsARuntimeExit(t *testing.T) {
	old := newParser
	newParser = func(any, ...kong.Option) (*kong.Kong, error) { return nil, errors.New("bad grammar") }
	t.Cleanup(func() { newParser = old })
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != errs.ExitRuntime {
		t.Fatalf("exit code = %d, want %d", code, errs.ExitRuntime)
	}
	if !strings.Contains(errb.String(), "bad grammar") || out.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}

func TestFailReportsAnUncodedErrorAsInternal(t *testing.T) {
	var out, errb bytes.Buffer
	rt := newRuntime(context.Background(), &Globals{}, &out, &errb)
	rt.format = output.JSON
	if code := rt.fail(errors.New("plain failure")); code != errs.ExitRuntime {
		t.Fatalf("exit code = %d, want %d", code, errs.ExitRuntime)
	}
	body := errorOf(t, errb.String())
	if body["code"] != errs.CodeInternal || !strings.Contains(body["message"].(string), "plain failure") {
		t.Fatalf("error = %v", body)
	}
}
