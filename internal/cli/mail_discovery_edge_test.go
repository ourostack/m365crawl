package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

func TestMailFailedRootResolutionBeforeArchiveIsNotAbsence(t *testing.T) {
	e := textEnv(t)
	old := outlookDefaultRoot
	t.Cleanup(func() { outlookDefaultRoot = old })
	outlookDefaultRoot = func() (string, error) { return "", os.ErrNotExist }
	for _, args := range [][]string{{}, {"mail", "list"}, {"search", "missing", "--source", "mail"}} {
		code, out, stderr := e.runDefault(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatalf("optional read-only unavailable exit %d: %s", code, stderr)
		}
		doc := decode(t, out)
		if len(args) == 0 {
			m := doc["sources"].([]any)[1].(map[string]any)
			if m["state"] != "unavailable" || !strings.Contains(m["note"].(string), "outlook_profile_unreadable") {
				t.Fatalf("overview root-resolution error hidden: %v", m)
			}
		} else if !strings.Contains(doc["note"].(string), "outlook_profile_unreadable") {
			t.Fatalf("source resolution error hidden in %v: %v", args, doc)
		}
	}
	rt := &runtime{outlookOn: true, outlookDefault: true}
	if c := rt.outlookStoreCheck(nil); !c.Warn || !strings.Contains(c.Detail, "outlook_profile_unreadable") {
		t.Fatalf("doctor mistook failed resolution for absent root: %+v", c)
	}
}

func TestDoctorLegacyEnumerationFailureKeepsUnknownState(t *testing.T) {
	rt, st, _ := mailRuntime(t, outlookStoreRoot(t, "HxStore.hxd"), true)
	old := outlookDiscover
	t.Cleanup(func() { outlookDiscover = old })
	outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return []outlookdesktop.Profile{{Name: "Main"}}, nil, nil, os.ErrPermission
	}
	if c := rt.mailReadableCheck(st); !c.Warn || !strings.Contains(c.Detail, "outlook_profile_unreadable") {
		t.Fatalf("legacy enumeration failure became no mail: %+v", c)
	}
}
