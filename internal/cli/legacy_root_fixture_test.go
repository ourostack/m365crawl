package cli

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func TestLegacyOutlookCreatedRootsPreservePathStates(t *testing.T) {
	check := func(t *testing.T) {
		t.Helper()
		for _, root := range []string{createdOWARoot(t), outlookProfiles(t), outlookStoreRoot(t, "HxStore.hxd")} {
			canonical, err := filepath.EvalSymlinks(root)
			if err != nil || root != canonical {
				t.Fatal("created_outlook_root_not_canonical")
			}
		}
		root := outlookProfiles(t)
		profiles, refused, err := outlookowa.Discover(root)
		if err != nil || len(profiles)+len(refused) != 0 {
			t.Fatal("created_empty_root_not_empty_discovery")
		}
		_, _, err = outlookowa.Discover(filepath.Join(root, "missing"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("derived_missing_root_changed_state")
		}
		file := filepath.Join(root, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal("created_file_setup_failed")
		}
		for _, path := range []string{file, filepath.Join(file, "below")} {
			if _, _, err := outlookowa.Discover(path); err == nil {
				t.Fatal("derived_non_directory_root_accepted")
			}
		}
	}
	t.Run("default-temp", check)
	if goruntime.GOOS != "windows" {
		t.Run("aliased-temp", func(t *testing.T) {
			target := createdOWARoot(t)
			link := filepath.Join(createdOWARoot(t), "alias")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal("owned_temp_alias_setup_failed")
			}
			t.Setenv("TMPDIR", link)
			check(t)
			for _, tc := range []struct {
				name string
				run  func(*testing.T)
			}{
				{"overview-account", TestOverviewMailFollowsTheAccount},
				{"search-missing-mail", TestSearchSaysWhyMailIsMissing},
				{"doctor-outlook", TestDoctorOutlookStoreCheck},
				{"mail-profile-count", TestMailProfileCount},
				{"missing-root-errors", TestOutlookRootMissingIsOneCodedError},
				{"overview-no-mail", TestOverviewSaysWhyThereIsNoMail},
			} {
				t.Run(tc.name, tc.run)
			}
		})
	}
}
