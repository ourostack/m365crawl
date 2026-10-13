//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func usageAccountName(i int) string {
	return fmt.Sprintf("Business_%08X-aaaa-BBBB-cccc-%012d", i, i)
}

// usageHome is a synthetic home with the default usage root and n matching account directories,
// each holding the usage and library leaves.
func usageHome(t *testing.T, accounts int) (home, root string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := discoveryHome
	t.Cleanup(func() { discoveryHome = original })
	discoveryHome = func() (string, error) { return home, nil }
	root = filepath.Join(home, "Library", "Application Support", "OneDrive", "settings", "ListSync")
	for i := range accounts {
		directory := filepath.Join(root, usageAccountName(i))
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, leaf := range []string{usageLeaf, "Microsoft.ListSync.db", "Unrelated.db"} {
			if err := os.WriteFile(filepath.Join(directory, leaf), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home, root
}

func fillDirectory(t *testing.T, directory string, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("n%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsageDiscoveryFinite(t *testing.T) {
	refused := func(t *testing.T, code string) {
		t.Helper()
		discovery, err := DiscoverOneDriveUsage(context.Background())
		if len(discovery.Inputs) != 0 || len(discovery.Outcomes) != 0 || usageCode(err) != code {
			t.Fatalf("discovery = %+v, %v; want no inputs and %s", discovery, err, code)
		}
	}
	t.Run("missing root is unavailable", func(t *testing.T) {
		home, root := usageHome(t, 0)
		refused(t, "onedrive_usage_unavailable")
		if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support"), 0o700); err != nil {
			t.Fatal(err)
		}
		refused(t, "onedrive_usage_unavailable")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		// An existing root with nothing to name is unavailable too, never healthy emptiness.
		refused(t, "onedrive_usage_unavailable")
		if err := os.WriteFile(filepath.Join(root, "Unrelated"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		refused(t, "onedrive_usage_unavailable")
	})
	t.Run("sixteen accounts, exact usage leaves only", func(t *testing.T) {
		_, root := usageHome(t, 16)
		discovery, err := DiscoverOneDriveUsage(context.Background())
		if err != nil || len(discovery.Inputs) != 16 || len(discovery.Outcomes) != 0 ||
			!slices.IsSortedFunc(discovery.Inputs, func(a, b UsageInput) int { return strings.Compare(a.Path, b.Path) }) {
			t.Fatalf("sixteen accounts = %+v, %v", discovery, err)
		}
		for i, input := range discovery.Inputs {
			// The account spelling, mixed case included, is kept exactly.
			if input.Path != filepath.Join(root, usageAccountName(i), usageLeaf) {
				t.Fatalf("input %d = %s", i, input.Path)
			}
		}
		if err := os.MkdirAll(filepath.Join(root, usageAccountName(16)), 0o700); err != nil {
			t.Fatal(err)
		}
		// Seventeen matching accounts register nothing, not the first sixteen.
		refused(t, "onedrive_usage_input_unqualified")
	})
	t.Run("root entry bound", func(t *testing.T) {
		_, root := usageHome(t, 2)
		fillDirectory(t, root, 0, 4094)
		discovery, err := DiscoverOneDriveUsage(context.Background())
		if err != nil || len(discovery.Inputs) != 2 {
			t.Fatalf("4096 root entries = %d inputs, %v", len(discovery.Inputs), err)
		}
		fillDirectory(t, root, 4094, 4095)
		refused(t, "onedrive_usage_input_unqualified")
	})
	t.Run("account entry bound", func(t *testing.T) {
		_, root := usageHome(t, 2)
		crowded := filepath.Join(root, usageAccountName(1))
		fillDirectory(t, crowded, 0, 4093)
		discovery, err := DiscoverOneDriveUsage(context.Background())
		if err != nil || len(discovery.Inputs) != 2 {
			t.Fatalf("4096 account entries = %d inputs, %v", len(discovery.Inputs), err)
		}
		fillDirectory(t, crowded, 4093, 4094)
		refused(t, "onedrive_usage_input_unqualified")
	})
	t.Run("unsupported shapes and missing leaves are scoped outcomes", func(t *testing.T) {
		_, root := usageHome(t, 2)
		personal := strings.Replace(usageAccountName(7), "Business_", "Personal_", 1)
		for _, name := range []string{"Business_short", "Business_" + strings.Repeat("g", 36), "Personal_synthetic", personal, "business_lowercase", "Unrelated"} {
			if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name, usageLeaf), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(filepath.Join(root, usageAccountName(1), usageLeaf)); err != nil {
			t.Fatal(err)
		}
		discovery, err := DiscoverOneDriveUsage(context.Background())
		want := []UsageDiscoveryOutcome{
			{Locator: filepath.Join(root, "Business_"+strings.Repeat("g", 36)), Code: "onedrive_usage_discovery_unsupported"},
			{Locator: filepath.Join(root, "Business_short"), Code: "onedrive_usage_discovery_unsupported"},
			{Locator: filepath.Join(root, "Personal_synthetic"), Code: "onedrive_usage_discovery_unsupported"},
			// A personal account folder is never read, even when the rest of its name is a GUID.
			{Locator: filepath.Join(root, personal), Code: "onedrive_usage_discovery_unsupported"},
			{Locator: filepath.Join(root, usageAccountName(1), usageLeaf), Code: "onedrive_usage_unavailable"},
		}
		slices.SortFunc(want, func(a, b UsageDiscoveryOutcome) int { return strings.Compare(a.Locator, b.Locator) })
		if err != nil || len(discovery.Inputs) != 1 || discovery.Inputs[0].Path != filepath.Join(root, usageAccountName(0), usageLeaf) ||
			!slices.Equal(discovery.Outcomes, want) {
			t.Fatalf("mixed discovery = %+v, %v", discovery, err)
		}
		// Only unsupported shapes: still explicit outcomes, never silent absence or an error.
		if err := os.RemoveAll(filepath.Join(root, usageAccountName(0))); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(root, usageAccountName(1))); err != nil {
			t.Fatal(err)
		}
		discovery, err = DiscoverOneDriveUsage(context.Background())
		if err != nil || len(discovery.Inputs) != 0 || len(discovery.Outcomes) != 4 {
			t.Fatalf("unsupported-only discovery = %+v, %v", discovery, err)
		}
	})
	for _, unsafe := range []string{"linked account", "file account", "linked leaf", "directory leaf"} {
		t.Run("unsafe "+unsafe, func(t *testing.T) {
			_, root := usageHome(t, 2)
			account := filepath.Join(root, usageAccountName(1))
			leaf := filepath.Join(account, usageLeaf)
			var err error
			switch unsafe {
			case "linked account":
				if err = os.Rename(account, account+".moved"); err == nil {
					err = os.Symlink(account+".moved", account)
				}
			case "file account":
				if err = os.RemoveAll(account); err == nil {
					err = os.WriteFile(account, nil, 0o600)
				}
			case "linked leaf":
				if err = os.Rename(leaf, leaf+".moved"); err == nil {
					err = os.Symlink(leaf+".moved", leaf)
				}
			case "directory leaf":
				if err = os.Remove(leaf); err == nil {
					err = os.Mkdir(leaf, 0o700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// One unsafe matching account or source refuses the family: no healthy peer is registered.
			refused(t, "onedrive_usage_input_unqualified")
		})
	}
	t.Run("faults and platform", func(t *testing.T) {
		usageHome(t, 1)
		home, read, closeFile, mac := discoveryHome, discoveryRead, jsonClose, discoveryMac
		t.Cleanup(func() { discoveryHome, discoveryRead, jsonClose, discoveryMac = home, read, closeFile, mac })
		discoveryRead = func(*os.File, int) ([]string, error) { return nil, os.ErrPermission }
		refused(t, "onedrive_usage_input_unqualified")
		discoveryRead = read
		jsonClose = func(f *os.File) error {
			if f.Name() == usageLeaf {
				return errors.Join(closeFile(f), os.ErrPermission)
			}
			return closeFile(f)
		}
		refused(t, "onedrive_usage_input_unqualified")
		jsonClose = closeFile
		discoveryHome = func() (string, error) { return "", errors.New("home fault") }
		refused(t, "onedrive_usage_input_unqualified")
		discoveryHome = home
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if discovery, err := DiscoverOneDriveUsage(cancelled); len(discovery.Inputs) != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled discovery: %+v %v", discovery, err)
		}
		discoveryMac = false
		refused(t, "onedrive_usage_default_unsupported")
	})
}

// The source inode is opened once and closed once, with the SQLite lock inside that lifetime; no
// validation opens it again. A separate writer is excluded after every validation.
func TestUsageValidationNeverReopensSource(t *testing.T) {
	path := usageSource(t)
	leaf := filepath.Base(path)
	open, closeFile, lock, stage := inputOpen, jsonClose, registryLock, registryStage
	t.Cleanup(func() { inputOpen, jsonClose, registryLock, registryStage = open, closeFile, lock, stage })
	var events []string
	inputOpen = func(dirfd int, name string, flags int, mode uint32) (int, error) {
		if name == leaf {
			events = append(events, "open")
		}
		return open(dirfd, name, flags, mode)
	}
	jsonClose = func(f *os.File) error {
		if f.Name() == leaf {
			events = append(events, "close")
		}
		return closeFile(f)
	}
	registryLock = func(f *os.File) (func() error, error) {
		unlock, err := lock(f)
		if err != nil {
			return nil, err
		}
		events = append(events, "lock")
		return func() error { events = append(events, "unlock"); return unlock() }, nil
	}
	registryStage = func(name string) {
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("writer admitted after %s validation: %s", name, output)
		}
	}
	err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
		_, err := c.ReadUsage(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "open,lock,unlock,close" {
		t.Fatalf("source-inode descriptor events = %s", got)
	}
}
