package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func TestLegacyOutlookCreatedRootsPreservePathStates(t *testing.T) {
	check := func(t *testing.T) {
		t.Helper()
		root := outlookRoot(t, "HxStore.hxd")
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil || root != canonical {
			t.Fatal("created_outlook_fixture_not_canonical")
		}
		_, _, err = outlookowa.Discover(filepath.Join(root, "missing"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("derived_missing_outlook_root_changed_state")
		}
		empty := createdOutlookRoot(t)
		canonical, err = filepath.EvalSymlinks(empty)
		if err != nil || empty != canonical {
			t.Fatal("created_empty_outlook_root_not_canonical")
		}
		profiles, refused, err := outlookowa.Discover(empty)
		if err != nil || len(profiles)+len(refused) != 0 {
			t.Fatal("created_empty_outlook_root_changed_state")
		}
		file := filepath.Join(empty, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal("created_file_setup_failed")
		}
		for _, path := range []string{file, filepath.Join(file, "below")} {
			if _, _, err := outlookowa.Discover(path); err == nil {
				t.Fatal("derived_non_directory_outlook_root_accepted")
			}
		}
	}
	t.Run("default-temp", check)
	if runtime.GOOS != "windows" {
		t.Run("aliased-temp", func(t *testing.T) {
			target, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal("owned_temp_canonicalization_failed")
			}
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal("owned_alias_parent_failed")
			}
			link := filepath.Join(parent, "alias")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal("owned_temp_alias_setup_failed")
			}
			t.Setenv("TMPDIR", link)
			check(t)
			t.Run("implicit", TestSyncOutlookImplicit)
			t.Run("implicit-failures", TestSyncOutlookImplicitNeverFails)
			t.Run("no-profiles", TestSyncOutlookNoProfiles)
			t.Run("implicit-without-teams", TestSyncOutlookImplicitWithoutTeams)
		})
	}
}
