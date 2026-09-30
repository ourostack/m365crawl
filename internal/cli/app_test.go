package cli

import (
	"bytes"
	"strings"
	"testing"
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
