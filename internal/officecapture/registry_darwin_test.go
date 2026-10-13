//go:build darwin

package officecapture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRegistryDefaultDiscoveryBounds(t *testing.T) {
	home := t.TempDir()
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	originalHome := discoveryHome
	t.Cleanup(func() { discoveryHome = originalHome })
	discoveryHome = func() (string, error) { return home, nil }
	if inputs, err := DiscoverRegistry(context.Background()); inputs != nil || registryCode(err) != "office_registry_unavailable" {
		t.Fatalf("missing default directory became healthy emptiness: %v", err)
	}
	directory := filepath.Join(home, "Library", "Group Containers", "UBF8T346G9.Office", "MicrosoftRegistrationDB")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if inputs, err := DiscoverRegistry(context.Background()); inputs != nil || registryCode(err) != "office_registry_unavailable" {
		t.Fatalf("empty default directory became healthy emptiness: %v", err)
	}
	for _, name := range []string{"MicrosoftRegistrationDB.reg", "MicrosoftRegistrationDB_.reg", "Other_x.reg", "MicrosoftRegistrationDB_x.reg-journal"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 16 {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("MicrosoftRegistrationDB_%02d.reg", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := DiscoverRegistry(context.Background())
	if err != nil || len(inputs) != 16 || inputs[0].Path != filepath.Join(directory, "MicrosoftRegistrationDB_00.reg") ||
		!slices.IsSortedFunc(inputs, func(a, b RegistryInput) int { return strings.Compare(a.Path, b.Path) }) {
		t.Fatalf("exact matching leaves = %v, %v", inputs, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "MicrosoftRegistrationDB_16.reg"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if inputs, err := DiscoverRegistry(context.Background()); inputs != nil || registryCode(err) != "office_registry_input_unqualified" {
		t.Fatalf("seventeen leaves admitted: %v", err)
	}
	crowded := filepath.Join(t.TempDir(), "crowded")
	crowded, _ = filepath.EvalSymlinks(filepath.Dir(crowded))
	crowded = filepath.Join(crowded, "MicrosoftRegistrationDB")
	if err := os.MkdirAll(crowded, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 4097 {
		if err := os.WriteFile(filepath.Join(crowded, fmt.Sprintf("n%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(crowded, directory); err != nil {
		t.Fatal(err)
	}
	if inputs, err := DiscoverRegistry(context.Background()); inputs != nil || registryCode(err) != "office_registry_input_unqualified" {
		t.Fatalf("directory above 4096 entries admitted: %v", err)
	}
	mac := discoveryMac
	t.Cleanup(func() { discoveryMac = mac })
	discoveryMac = false
	if inputs, err := DiscoverRegistry(context.Background()); inputs != nil || registryCode(err) != "office_registry_default_unsupported" {
		t.Fatalf("non-Mac default discovery admitted: %v", err)
	}
}

func TestRegistryValidationNeverReopensSource(t *testing.T) {
	path := registrySource(t)
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
	var stages []string
	registryStage = func(name string) {
		stages = append(stages, name)
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("writer admitted after %s validation: %s", name, output)
		}
	}
	err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
		_, err := c.ReadRegistry(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "open,lock,unlock,close" {
		t.Fatalf("source-inode descriptor events = %s", got)
	}
	want := []string{"locked", "copied", "before-reader", "after-reader", "after-callback", "cleanup"}
	if !slices.Equal(stages, want) {
		t.Fatalf("validation stages = %v, want %v", stages, want)
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover: %s", output)
	}
}

func TestRegistryAnchoredLateSubstitution(t *testing.T) {
	for _, tc := range []struct{ binding, stage string }{
		{"source", "copied"}, {"parent", "copied"}, {"companion", "copied"}, {"new-companion", "copied"},
		{"source", "before-reader"}, {"parent", "after-reader"}, {"companion", "after-callback"},
	} {
		t.Run(tc.binding+"@"+tc.stage, func(t *testing.T) {
			path := registrySource(t)
			if err := os.WriteFile(path+"-journal", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			stage := registryStage
			t.Cleanup(func() { registryStage = stage })
			mutated := false
			registryStage = func(name string) {
				if name != tc.stage || mutated {
					return
				}
				mutated = true
				replacement := path + ".replacement"
				data, err := os.ReadFile(path) //nolint:gosec // synthetic fixture
				if err != nil {
					t.Error(err)
					return
				}
				switch tc.binding {
				case "source":
					err = os.WriteFile(replacement, data, 0o600) //nolint:gosec // test-owned synthetic path
					if err == nil {
						err = os.Rename(replacement, path)
					}
				case "parent":
					parent := filepath.Dir(path)
					err = os.Rename(parent, parent+".moved")
					if err == nil {
						err = os.Mkdir(parent, 0o700)
					}
					if err == nil {
						err = os.WriteFile(path, data, 0o600) //nolint:gosec // test-owned synthetic path
					}
				case "companion":
					err = os.WriteFile(replacement, nil, 0o600)
					if err == nil {
						err = os.Rename(replacement, path+"-journal")
					}
				case "new-companion":
					err = os.WriteFile(path+"-wal", nil, 0o600)
				}
				if err != nil {
					t.Error(err)
				}
			}
			published := false
			err := WithRegistryCapture(context.Background(), RegistryInput{Path: path}, func(c *RegistryCapture) error {
				if _, err := c.ReadRegistry(context.Background()); err != nil {
					return err
				}
				published = true
				return nil
			})
			if !mutated || err == nil || registryCode(err) != "office_registry_capture_failed" {
				t.Fatalf("substituted %s binding admitted: mutated=%t published=%t err=%v", tc.binding, mutated, published, err)
			}
		})
	}
}

func TestRegistryEdgeFaults(t *testing.T) {
	path := registrySource(t)
	statat := registryStatat
	t.Cleanup(func() { registryStatat = statat })
	registryStatat = func(dirfd int, name string, stat *unix.Stat_t, flags int) error {
		if strings.HasSuffix(name, "-shm") {
			return unix.EACCES
		}
		return statat(dirfd, name, stat, flags)
	}
	if _, err := ObserveRegistry(context.Background(), path); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatalf("companion inspection fault became evidence: %v", err)
	}
}

func TestRegistryLockSafeValidationFaults(t *testing.T) {
	path := registrySource(t)
	input, err := openInput(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.close() })
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := input.validateSQLite(cancelled); err == nil {
		t.Fatal("cancelled validation passed")
	}
	directory, err := openInput(filepath.Dir(path), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.close() })
	if err := directory.validateSQLite(context.Background()); err == nil {
		t.Fatal("directory accepted as a bound source")
	}
	if _, err := registryCompanions(context.Background(), directory); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("directory companions inspected")
	}
	stamp := input.stamps[1]
	input.stamps[1] = "changed"
	if err := input.validateSQLite(context.Background()); err == nil {
		t.Fatal("changed held stamp passed")
	}
	input.stamps[1] = stamp
	stat := inputStat
	t.Cleanup(func() { inputStat = stat })
	calls := 0
	inputStat = func(fd int, info *unix.Stat_t) error {
		calls++
		if calls == 3 { // root signature, first edge signature, then the first edge's held stat
			return unix.EIO
		}
		return stat(fd, info)
	}
	if err := input.validateSQLite(context.Background()); err == nil {
		t.Fatal("held stat fault passed")
	}
	inputStat = stat
	if err := input.validateSQLite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := input.validateSQLite(context.Background()); err == nil {
		t.Fatal("missing anchored edge passed")
	}
}
