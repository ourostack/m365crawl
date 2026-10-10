//go:build e2e

package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyOWAFixture(t *testing.T, variant string) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "testdata", "outlook-owa-fixture", variant)
	dst := filepath.Join(root, "Default")
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(path) //nolint:gosec // committed synthetic fixture
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // test-owned directory
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMailOWACommands(t *testing.T) {
	e := newEnv(t)
	root := copyOWAFixture(t, "mail")
	run := func(args ...string) result {
		return e.cmd(append([]string{"--outlook-root", root, "--max-age", "0"}, args...)...)
	}
	sync := ok(t, run("sync"))
	if strings.Contains(run("status").stdout, "credential") {
		t.Fatal("credential decoy leaked")
	}
	sources := sync["sources"].([]any)
	native := 0
	for _, raw := range sources {
		s := raw.(map[string]any)
		if strings.HasPrefix(s["source"].(string), "outlook|") {
			native++
			if s["source"] != "outlook|Default|mail" {
				t.Fatalf("OWA must emit mail only: %v", s)
			}
		}
	}
	if native != 1 {
		t.Fatalf("native source count = %d", native)
	}
	its, _ := list(t, run("mail", "list"))
	if len(its) != 5 {
		t.Fatalf("cached count = %d", len(its))
	}
	var id string
	for _, it := range its {
		if it["subject"] == "Synthetic Normal" {
			id = it["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("synthetic normal message absent")
	}
	show := ok(t, run("mail", "show", id))
	recipients := show["recipients"].([]any)
	if len(recipients) != 4 || show["to_me"] != false || show["in_reply_to"] != nil || show["body_text"] != "Synthetic normal cached HTML" {
		t.Fatalf("normal mapping = %v", show)
	}
	for i, want := range []string{"to", "to", "cc", "bcc"} {
		r := recipients[i].(map[string]any)
		if r["role"] != want || r["kind_raw"] != float64(0) {
			t.Fatalf("recipient %d = %v", i, r)
		}
	}
	a := show["attachments"].([]any)[0].(map[string]any)
	if a["downloaded"] != false || a["size"] != float64(0) {
		t.Fatalf("metadata-only attachment = %v", a)
	}
	thread := ok(t, run("mail", "thread", id))
	if thread["grouping"] != "subject" || thread["count"] != float64(1) {
		t.Fatalf("conversation = %v", thread)
	}
	folders, _ := list(t, run("mail", "folders"))
	if len(folders) != 7 {
		t.Fatalf("folders = %d", len(folders))
	}
	unread := ok(t, run("mail", "unread"))
	if unread["unread_total"] != float64(1) {
		t.Fatalf("unread = %v", unread)
	}
	hits, _ := list(t, run("search", "Synthetic", "--source", "mail"))
	if len(hits) != 5 {
		t.Fatalf("search hits = %d", len(hits))
	}
	status := ok(t, run("status"))["mail"].(map[string]any)
	if status["state"] != "ok" || status["messages"] != float64(5) || status["folders"] != float64(7) {
		t.Fatalf("status = %v", status)
	}
	overview := ok(t, run())
	if overview["sources"].([]any)[1].(map[string]any)["messages"] != float64(5) {
		t.Fatalf("overview = %v", overview)
	}
	doctor := ok(t, run("doctor"))
	found := false
	for _, raw := range doctor["checks"].([]any) {
		c := raw.(map[string]any)
		if c["name"] == "outlook_store" {
			found = true
			if c["warn"] == true || !strings.Contains(c["detail"].(string), "calendar is unsupported on OWA") {
				t.Fatalf("doctor = %v", c)
			}
		}
	}
	if !found {
		t.Fatal("doctor omitted Outlook source")
	}
}

func TestMailOWACodedLayoutRefusal(t *testing.T) {
	for _, variant := range []string{"unknown", "multiple", "missing-store"} {
		t.Run(variant, func(t *testing.T) {
			e := newEnv(t)
			res := e.cmd("--outlook-root", copyOWAFixture(t, variant), "sync")
			mustExit(t, res, 1)
			if !strings.Contains(res.stdout+res.stderr, `"code":"outlook_mail_layout_unsupported"`) {
				t.Fatalf("missing coded refusal: %s %s", res.stdout, res.stderr)
			}
			its, _ := list(t, e.cmd("--max-age", "0", "mail", "list"))
			if len(its) != 0 {
				t.Fatal("refusal archived mail")
			}
		})
	}
}
