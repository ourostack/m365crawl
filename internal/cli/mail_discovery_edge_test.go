package cli

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

func TestCreatedOWARootIsCanonical(t *testing.T) {
	check := func(t *testing.T) {
		t.Helper()
		root := createdOWARoot(t)
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil || root != canonical {
			t.Fatal("created_owa_root_not_canonical")
		}
	}
	t.Run("default", check)
	if goruntime.GOOS != "windows" {
		t.Run("aliased-temp", func(t *testing.T) {
			target := createdOWARoot(t)
			link := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal("synthetic_temp_alias_failed")
			}
			t.Setenv("TMPDIR", link)
			check(t)
		})
	}
}

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

func TestDoctorLegacyFailedDefaultResolutionIsUnknown(t *testing.T) {
	oldPlatform, oldRoot := mailPlatform, outlookDefaultRoot
	t.Cleanup(func() { mailPlatform, outlookDefaultRoot = oldPlatform, oldRoot })
	mailPlatform = "darwin"
	rt := &runtime{outlookOn: true, outlookDefault: true}
	for _, failure := range []error{os.ErrNotExist, os.ErrPermission, outlookdesktop.ErrRootNotFound, errors.New("synthetic-private-root-error")} {
		outlookDefaultRoot = func() (string, error) { return "", failure }
		c := rt.outlookStoreCheck(nil)
		if !c.OK || !c.Warn || !strings.Contains(c.Detail, "outlook_profile_unreadable") || strings.Contains(c.Detail+c.Fix, "HxStore") || strings.Contains(c.Detail+c.Fix, "synthetic-private") {
			t.Fatalf("legacy failed resolution became layout/absence: %+v", c)
		}
	}
}

func TestMailUnsupportedOptionalDefaultIsNotUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name        string
		s           outlookMailSource
		optional    bool
		wantUnknown bool
	}{
		{"legacy-unsupported-default", outlookMailSource{err: outlookdesktop.ErrNotSupported}, true, false},
		{"native-unsupported-default", outlookMailSource{owa: true, err: outlookdesktop.ErrNotSupported}, true, true},
		{"explicit-unsupported-root", outlookMailSource{root: "synthetic", err: outlookdesktop.ErrNotSupported}, false, true},
		{"legacy-unreadable-default", outlookMailSource{err: os.ErrPermission}, true, true},
		{"legacy-missing-resolution", outlookMailSource{err: os.ErrNotExist}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := tc.s.problem(tc.optional)
			if (problem != nil) != tc.wantUnknown || problem != nil && problem.Code != "outlook_profile_unreadable" {
				t.Fatal("optional source classification mismatch")
			}
		})
	}
}
