package cli

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func TestMailDiscoveryFailuresRemainUnknownAcrossReadSurfaces(t *testing.T) {
	for _, failure := range []string{"root resolution", "enumeration permission", "refused profile"} {
		t.Run(failure, func(t *testing.T) {
			e := textEnv(t)
			e.emptyArchive()
			oldRoot, oldDiscover := outlookDefaultRoot, outlookNativeDiscover
			t.Cleanup(func() { outlookDefaultRoot, outlookNativeDiscover = oldRoot, oldDiscover })
			root := createdOWARoot(t)
			outlookDefaultRoot = func() (string, error) { return root, nil }
			outlookNativeDiscover = func(string) ([]outlookowa.Profile, []outlookowa.SkippedProfile, error) {
				if failure == "refused profile" {
					return nil, []outlookowa.SkippedProfile{{Name: "Synthetic", Err: errors.New("synthetic-private-suffix")}}, nil
				}
				return nil, nil, os.ErrPermission
			}
			if failure == "root resolution" {
				outlookDefaultRoot = func() (string, error) { return "", errors.New("synthetic-private-suffix") }
			}
			for _, args := range [][]string{{"status"}, {}, {"search", "missing", "--source", "mail"}, {"mail", "list"}, {"mail", "folders"}, {"mail", "unread"}} {
				code, out, stderr := e.runDefault(append([]string{"--json", "--max-age", "0"}, args...)...)
				if code != 0 {
					t.Fatalf("read-only optional source failed whole command %v: %d %s", args, code, stderr)
				}
				doc := decode(t, out)
				if len(args) > 0 && args[0] == "status" {
					m := mailBlockOf(t, doc)
					if m["state"] != "unavailable" || m["source_error"] == nil {
						t.Fatalf("failure became absence in status: %v", m)
					}
				} else if len(args) == 0 {
					m := doc["sources"].([]any)[1].(map[string]any)
					if m["state"] != "unavailable" || !strings.Contains(m["note"].(string), "outlook_profile_unreadable") {
						t.Fatalf("failure became absence in overview: %v", m)
					}
				} else if note, _ := doc["note"].(string); !strings.Contains(note, "outlook_profile_unreadable") || strings.Contains(note, "no supported Outlook") {
					t.Fatalf("failure became absence in %v: %v", args, doc)
				}
				if strings.Contains(out+stderr, "synthetic-private-suffix") {
					t.Fatal("unsafe source diagnostic leaked")
				}
			}
		})
	}
}

func TestMailMissingLocalAppDataIsNotNoProfile(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("actual native Windows default-root resolution")
	}
	e := textEnv(t)
	e.emptyArchive()
	t.Setenv("LOCALAPPDATA", "")
	code, out, stderr := e.runDefault("--max-age", "0", "status")
	if code != 0 {
		t.Fatalf("status exit %d: %s", code, stderr)
	}
	if m := mailBlockOf(t, decode(t, out)); m["state"] != "unavailable" {
		t.Fatalf("unresolved root treated as no profile: %v", m)
	}
}

func TestOutlookRootRepairSupportsBothSourceShapes(t *testing.T) {
	e := textEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	code, _, stderr := e.run("--json", "--outlook-root", missing, "sync")
	er := errorOf(t, stderr)
	fix, _ := er["fix"].(string)
	if code != 3 || er["code"] != "outlook_root_missing" || !strings.Contains(fix, "EBWebView") || !strings.Contains(fix, "HxStore.hxd") || !strings.Contains(fix, "none") {
		t.Fatalf("repair excludes supported Windows source: exit %d %v", code, er)
	}
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-calendar-fixture", "calendar"), filepath.Join(root, "Default"))
	if code, _, stderr := e.run("--outlook-root", root, "sync"); code != 0 {
		t.Fatalf("supported EBWebView repair did not work: %d %s", code, stderr)
	}
}
