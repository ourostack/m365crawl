package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func TestMailDiscoveryUnknownPreservesHealthyArchivedResults(t *testing.T) {
	e := textEnv(t)
	seedMail(t, e, seedMessages())
	root := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(root, "Main"))
	old := outlookNativeDiscover
	t.Cleanup(func() { outlookNativeDiscover = old })
	outlookNativeDiscover = func(path string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
		profiles, _, err := old(path)
		return profiles, []outlookowa.SkippedProfile{{Name: "Unreadable", Err: os.ErrPermission}}, err
	}
	for _, args := range [][]string{{"status"}, {}, {"search", "Quarterly", "--source", "mail"}} {
		code, out, stderr := e.run(append([]string{"--outlook-root", root, "--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatalf("healthy archive hidden by sibling: %d %s", code, stderr)
		}
		doc := decode(t, out)
		switch {
		case len(args) == 0:
			m := doc["sources"].([]any)[1].(map[string]any)
			if m["state"] != "unavailable" || m["messages"] != float64(6) {
				t.Fatalf("overview lost known counts/unknown refresh: %v", m)
			}
		case args[0] == "status":
			m := mailBlockOf(t, doc)
			if m["state"] != "unavailable" || m["messages"] != float64(6) || m["source_error"].(map[string]any)["code"] != "outlook_profile_unreadable" {
				t.Fatalf("status lost known counts/unknown refresh: %v", m)
			}
		default:
			if doc["count"] != float64(3) || !strings.Contains(doc["note"].(string), "outlook_profile_unreadable") {
				t.Fatalf("search lost healthy results or sibling diagnosis: %v", doc)
			}
		}
	}
	code, out, stderr := e.run("--format", "text", "--outlook-root", root, "--max-age", "0", "status")
	if code != 0 || !strings.Contains(out, "unavailable:") || !strings.Contains(out, "outlook_profile_unreadable") {
		t.Fatalf("text status hides source diagnosis: %d %s %s", code, out, stderr)
	}
	doc := mailJSON(t, e, "--outlook-root", root, "mail", "list", "--from", "nobody")
	if !strings.Contains(doc["note"].(string), "outlook_profile_unreadable") {
		t.Fatalf("empty filtered mail hid source refusal: %v", doc)
	}
}

func TestMailOptionalResolvedAbsenceAndSourceOffRemainNonFailures(t *testing.T) {
	e := textEnv(t)
	e.emptyArchive()
	old := outlookDefaultRoot
	t.Cleanup(func() { outlookDefaultRoot = old })
	absent := filepath.Join(t.TempDir(), "absent")
	outlookDefaultRoot = func() (string, error) { return absent, nil }
	code, out, stderr := e.runDefault("--max-age", "0", "status")
	if code != 0 || mailBlockOf(t, decode(t, out))["state"] != "no_profile" {
		t.Fatalf("optional resolved missing root treated as failure: %d %s %s", code, out, stderr)
	}
	outlookDefaultRoot = func() (string, error) { return "", os.ErrPermission }
	code, out, stderr = e.runDefault("--outlook-root", "none", "--max-age", "0", "status")
	m := mailBlockOf(t, decode(t, out))
	if code != 0 || m["state"] != "skipped" || m["source_error"] != nil {
		t.Fatalf("disabled source became error: %d %v %s", code, m, stderr)
	}
}

func TestMailLegacySkippedProfileIsNotNoProfile(t *testing.T) {
	e := textEnv(t)
	e.emptyArchive()
	root := outlookStoreRoot(t, "HxStore.hxd")
	old := outlookDiscover
	t.Cleanup(func() { outlookDiscover = old })
	outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return []outlookdesktop.Profile{{Name: "Main"}}, nil, []outlookdesktop.SkippedProfile{{Name: "Unreadable", Reason: "unreadable"}}, nil
	}
	m := mailBlockOf(t, mailJSON(t, e, "--outlook-root", root, "status"))
	if m["state"] != "unavailable" || m["source_error"] == nil {
		t.Fatalf("legacy sibling refusal became absence: %v", m)
	}
}
