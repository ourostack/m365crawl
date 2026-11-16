package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A --dry-run write prints the outcome it would have (planned, naming who would see it) and touches no archive.
func TestWritePreviewIsAPlannedOutcomeThatSendsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	if code := Main([]string{"writes", "send-teams-message", "--chat", "19:chat@unq.gbl.spaces", "--text", "hi", "--why", "Jo asked", "--dry-run", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errb.String())
	}
	var p map[string]any
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatalf("preview is not one JSON document: %v\n%s", err, out.String())
	}
	if p["ax"] != "outcome/1" || p["state"] != "planned" || p["effect"] != "others" {
		t.Fatalf("preview = %v, want an outcome/1 planned for others", p)
	}
	audience := p["audience"].(map[string]any)
	if audience["only"] != false || !strings.Contains(audience["who"].(string), "19:chat@unq.gbl.spaces") {
		t.Fatalf("audience = %v, want everyone in the chat by its id", audience)
	}
	if _, err := os.Stat(filepath.Join(home, ".m365crawl")); !os.IsNotExist(err) {
		t.Fatalf("a preview touched the archive folder: %v", err)
	}
}

func TestWritePreviewInTextSaysWhoWillSeeIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := Main([]string{"--format", "text", "writes", "send-mail", "--to", "ana@example.com", "--text", "hi", "--why", "Jo asked", "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "ana@example.com") || !strings.Contains(out.String(), "they will see it") {
		t.Fatalf("text preview = %q, want the recipient and who will see it", out.String())
	}
}

func TestWritePreviewNamesEachDoor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := [][]string{
		{"writes", "send-mail", "--text", "hi", "--why", "Jo asked", "--dry-run", "--json"},
		{"writes", "reply-mail", "--message", "<m@example.com>", "--text", "hi", "--why", "Jo asked", "--dry-run", "--json"},
		{"writes", "reply-all-mail", "--message", "<m@example.com>", "--text", "hi", "--why", "Jo asked", "--dry-run", "--json"},
		{"writes", "add-reaction", "--chat", "19:chat@unq.gbl.spaces", "--message", "1", "--reaction", "👍", "--why", "Jo asked", "--dry-run", "--json"},
		{"writes", "remove-reaction", "--chat", "19:chat@unq.gbl.spaces", "--message", "1", "--reaction", "👍", "--why", "Jo asked", "--dry-run", "--json"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 0 {
			t.Fatalf("%v: exit code = %d, stderr = %q", args, code, errb.String())
		}
		if !strings.Contains(out.String(), `"state":"planned"`) {
			t.Fatalf("%v: preview = %q, want a planned outcome", args, out.String())
		}
	}
}

// Without a reason nothing is previewed or sent, and the error names --why.
func TestWriteWithoutWhyRefusesAndNamesWhy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"writes", "send-teams-message", "--chat", "19:chat@unq.gbl.spaces", "--text", "hi", "--dry-run"},
		{"writes", "send-teams-message", "--chat", "19:chat@unq.gbl.spaces", "--text", "hi"},
	} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit code = %d, want 2", args, code)
		}
		if !strings.Contains(errb.String(), "--why") || out.Len() != 0 {
			t.Fatalf("%v: stdout = %q, stderr = %q; want nothing on stdout and --why on stderr", args, out.String(), errb.String())
		}
	}
}

// A preview needs the chat or mail it acts on: with no id it refuses rather than previewing nothing.
func TestWritePreviewRefusesAMissingTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := Main([]string{"writes", "reply-mail", "--text", "hi", "--why", "Jo asked", "--dry-run"}, &out, &errb); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "needs the mail") || out.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want the missing mail named on stderr", out.String(), errb.String())
	}
}
