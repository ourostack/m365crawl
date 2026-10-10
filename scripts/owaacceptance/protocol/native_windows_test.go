package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedReviewedBinaryRefusesWriteAndReplacement(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	// #nosec G304 -- Current validation executable only, never private source/cache.
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal("test_artifact_read_failed")
	}
	path := filepath.Join(t.TempDir(), "pinned.exe")
	// #nosec G703 -- Exact t.TempDir validation executable copy; not a source/cache-derived filename.
	if os.WriteFile(path, data, 0o600) != nil {
		t.Fatal("test_artifact_copy_failed")
	}
	digest := sha256.Sum256(data)
	pin, err := PinBinary(path, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal("test_artifact_pin_failed")
	}
	if os.WriteFile(path, []byte("synthetic"), 0o600) == nil {
		t.Error("pinned_binary_write_accepted")
	}
	if os.Rename(path, path+".replacement") == nil {
		t.Error("pinned_binary_replace_accepted")
	}
	if pin.Close() != nil {
		t.Fatal("binary_pin_close_failed")
	}
	if os.Rename(path, path+".released") != nil {
		t.Fatal("released_binary_rename_failed")
	}
}

func TestGuardRejectsAbsentForgedAndStaleOwnership(t *testing.T) {
	original := os.Args
	os.Args = []string{"binary", "-test.run=^" + TestName + "$", "-test.timeout=4m"}
	t.Cleanup(func() { os.Args = original })
	for _, mode := range []string{"absent", "forged", "stale"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if MakePrivateDirectory(dir) != nil {
				t.Fatal("private_record_directory_failed")
			}
			t.Setenv(OwnerEnv, filepath.Join(dir, AdoptedName))
			if mode != "absent" {
				owner := validOwner()
				if mode == "stale" {
					owner.Supervisor.Created++
					owner.Child.Created++
				}
				initial := owner
				initial.Adopted = false
				initial.Child = Process{}
				for name, value := range map[string]Ownership{OwnerName: initial, AdoptedName: owner} {
					data, _ := Encode(value)
					if WriteProtectedNew(filepath.Join(dir, name), data) != nil {
						t.Fatal("synthetic_record_write_failed")
					}
				}
			}
			if _, err := CurrentGuard(); err == nil {
				t.Fatal("invalid_guard_accepted")
			}
		})
	}
}

func TestGuardRequiresExactSelectedTest(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	for _, args := range [][]string{
		{"binary", "-test.run=^" + TestName + "$", "-test.timeout=4m"},
		{"binary", "-test.run=unmatched"},
		{"binary", "-test.run=^" + TestName + "$", "-test.run=unmatched"},
		{"binary", "-test.run=^" + TestName + "$", "-test.cpuprofile=private"},
	} {
		os.Args = args
		err := SelectedTest()
		if len(args) == 3 && args[2] == "-test.timeout=4m" {
			if err != nil {
				t.Fatal("exact_selection_refused")
			}
		} else if err == nil {
			t.Fatal("unapproved_selection_accepted")
		}
	}
}
