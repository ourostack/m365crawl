package outlookowa

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func restoreSourceIO(t *testing.T) {
	t.Helper()
	read, stat, abs, walk, info := sourceReadDir, sourceLstat, sourceAbs, sourceWalkDir, sourceEntryInfo
	identity, rel := fingerprintIdentity, fingerprintRel
	t.Cleanup(func() {
		sourceReadDir, sourceLstat, sourceAbs, sourceWalkDir, sourceEntryInfo = read, stat, abs, walk, info
		fingerprintIdentity, fingerprintRel = identity, rel
	})
}

func TestDiscoverSkipsPlainFilesMissingIndexedDBAndSortsFailures(t *testing.T) {
	restoreSourceIO(t)
	root := sourceTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "plain-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"No IndexedDB", "A denied", "B denied"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureProfile(t, root, "Default", "mail", false)
	read := sourceReadDir
	sourceReadDir = func(path string) ([]os.DirEntry, error) {
		entries, err := read(path)
		if path == root {
			// Preserve real entries, but model a filesystem enumeration in reverse order.
			slices.Reverse(entries)
		}
		return entries, err
	}
	stat := sourceLstat
	sourceLstat = func(path string) (os.FileInfo, error) {
		if path == filepath.Join(root, "A denied") || path == filepath.Join(root, "B denied") {
			return nil, fs.ErrPermission
		}
		return stat(path)
	}
	found, skipped, err := Discover(root)
	if err != nil || len(found) != 1 || found[0].Name != "Default" || len(skipped) != 2 || skipped[0].Name != "A denied" || skipped[1].Name != "B denied" {
		t.Fatalf("independent sorted discovery: %+v %+v %v", found, skipped, err)
	}
	for _, s := range skipped {
		if !errors.Is(s.Err, fs.ErrPermission) {
			t.Fatalf("profile access failure was discarded: %v", s.Err)
		}
	}
}

func TestDiscoverMetadataFaultsStayFailures(t *testing.T) {
	for _, where := range []string{"root", "blob", "validation-walk", "validation-info", "absolute-path"} {
		t.Run(where, func(t *testing.T) {
			restoreSourceIO(t)
			p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", true)
			root := filepath.Dir(p.Dir)
			switch where {
			case "root", "blob":
				stat := sourceLstat
				sourceLstat = func(path string) (os.FileInfo, error) {
					if where == "root" && path == root || where == "blob" && path == p.Source.BlobDir {
						return nil, fs.ErrPermission
					}
					return stat(path)
				}
			case "validation-walk":
				sourceWalkDir = func(path string, fn fs.WalkDirFunc) error {
					return fn(path, nil, fs.ErrPermission)
				}
			case "validation-info":
				info := sourceEntryInfo
				sourceEntryInfo = func(e fs.DirEntry) (fs.FileInfo, error) {
					if e.Name() == "CURRENT" {
						return nil, fs.ErrNotExist
					}
					return info(e)
				}
			case "absolute-path":
				sourceAbs = func(string) (string, error) { return "", fs.ErrPermission }
			}
			found, skipped, err := Discover(root)
			if len(found) != 0 || err == nil && len(skipped) != 1 {
				t.Fatalf("metadata fault became an eligible source: %+v %+v %v", found, skipped, err)
			}
			if err == nil {
				err = skipped[0].Err
			}
			want := fs.ErrPermission
			if where == "validation-info" {
				want = fs.ErrNotExist
			}
			if !errors.Is(err, want) || err.Error() != "outlookowa: source_unreadable" && err.Error() != "outlookowa: profile_unreadable" {
				t.Fatalf("metadata fault was hidden or unsafe: %v", err)
			}
		})
	}
}

func TestDiscoverRejectsNondirectoryRootAndVanishedProfile(t *testing.T) {
	restoreSourceIO(t)
	root := sourceTempDir(t)
	file := filepath.Join(root, "not-a-root")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, s, err := Discover(file); err == nil || len(p)+len(s) != 0 {
		t.Fatal("nondirectory root became an empty successful discovery")
	}
	gone := filepath.Join(root, "vanished")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	read := sourceReadDir
	sourceReadDir = func(path string) ([]os.DirEntry, error) {
		entries, err := read(path)
		if path == root {
			if removeErr := os.Remove(gone); removeErr != nil {
				return nil, removeErr
			}
		}
		return entries, err
	}
	p, s, err := Discover(root)
	if err != nil || len(p) != 0 || len(s) != 1 || s[0].Name != "vanished" || !errors.Is(s[0].Err, fs.ErrNotExist) {
		t.Fatalf("vanished profile silently dropped: %+v %+v %v", p, s, err)
	}
}

func TestFingerprintNoBlobAndConcurrentMetadataFailures(t *testing.T) {
	for _, where := range []string{"no-blob", "walk", "stat", "identity", "relative-path"} {
		t.Run(where, func(t *testing.T) {
			restoreSourceIO(t)
			p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", false)
			fp, fresh, err := Fingerprint(p)
			if err != nil || len(fp) != 64 || fresh.IsZero() || p.Source.BlobDir != "" {
				t.Fatalf("no-blob fingerprint: %q %v %v", fp, fresh, err)
			}
			if where == "no-blob" {
				return
			}
			validated := false
			walk := sourceWalkDir
			sourceWalkDir = func(path string, fn fs.WalkDirFunc) error {
				if !validated {
					err := walk(path, fn)
					validated = true
					return err
				}
				if where == "walk" {
					return fn(path, nil, fs.ErrPermission)
				}
				return walk(path, fn)
			}
			if where == "stat" {
				info := sourceEntryInfo
				sourceEntryInfo = func(e fs.DirEntry) (fs.FileInfo, error) {
					if validated && e.Name() == "CURRENT" {
						return nil, fs.ErrNotExist
					}
					return info(e)
				}
			}
			if where == "identity" {
				fingerprintIdentity = func(string, os.FileInfo) (string, error) {
					return "", sourceFailure("source_unreadable", fs.ErrPermission)
				}
			}
			if where == "relative-path" {
				fingerprintRel = func(string, string) (string, error) { return "", fs.ErrInvalid }
			}
			next, at, err := Fingerprint(p)
			if err == nil || next != "" || !at.IsZero() || err.Error() != "outlookowa: source_unreadable" {
				t.Fatalf("concurrent metadata error vouched for source: %q %v %v", next, at, err)
			}
		})
	}
}

func TestValidateRegularLeafAndNonregularAncestor(t *testing.T) {
	root := sourceTempDir(t)
	file := filepath.Join(root, "leaf")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePath(file, false); err != nil {
		t.Fatalf("regular leaf rejected: %v", err)
	}
	if err := ValidateSource(teamsdesktop.Source{LevelDBDir: filepath.Join(file, "child")}); err == nil {
		t.Fatal("nondirectory ancestor accepted")
	}
}

func TestDiscoverLateSourceFailures(t *testing.T) {
	for _, where := range []string{"indexeddb-enumeration", "matched-origin-validation"} {
		t.Run(where, func(t *testing.T) {
			restoreSourceIO(t)
			root := sourceTempDir(t)
			p := fixtureProfile(t, root, "Default", "mail", false)
			if err := ValidateSource(p.Source); err != nil {
				t.Fatal("synthetic_source_prevalidation_failed")
			}
			idb := filepath.Join(p.Dir, "IndexedDB")
			read, stat := sourceReadDir, sourceLstat
			idbStats, faults := 0, 0
			enumerated := false
			sourceLstat = func(path string) (os.FileInfo, error) {
				if path == idb {
					idbStats++
				}
				if where == "matched-origin-validation" && path == p.Source.LevelDBDir {
					if !enumerated || idbStats < 2 {
						t.Fatal("origin_fault_before_indexeddb_validation")
					}
					faults++
					return nil, &fs.PathError{Op: "synthetic-private-operation", Path: "synthetic-private-path", Err: fs.ErrPermission}
				}
				return stat(path)
			}
			sourceReadDir = func(path string) ([]os.DirEntry, error) {
				if path == idb {
					if idbStats < 2 {
						t.Fatal("enumeration_fault_before_indexeddb_validation")
					}
					enumerated = true
					if where == "indexeddb-enumeration" {
						faults++
						return nil, &fs.PathError{Op: "synthetic-private-operation", Path: "synthetic-private-path", Err: fs.ErrPermission}
					}
				}
				return read(path)
			}
			found, refused, err := Discover(root)
			if faults != 1 || !enumerated || err != nil || len(found) != 0 || len(refused) != 1 {
				t.Fatal("late_discovery_fault_returned_partial_or_success")
			}
			wantReason, wantError := "unsafe_source", "outlookowa: source_unreadable"
			if where == "indexeddb-enumeration" {
				wantReason, wantError = "unreadable", "outlookowa: profile_unreadable"
			}
			if refused[0].Reason != wantReason || refused[0].Err.Error() != wantError || !errors.Is(refused[0].Err, fs.ErrPermission) {
				t.Fatal("late_discovery_fault_not_fixed_and_preserved")
			}
			sourceReadDir, sourceLstat = read, stat
			found, refused, err = Discover(root)
			if err != nil || len(found) != 1 || len(refused) != 0 || found[0].Source != p.Source {
				t.Fatal("restored_discovery_could_not_retry")
			}
		})
	}
}

type lateUnsafeInfo struct{ fs.FileInfo }

func (i lateUnsafeInfo) Mode() fs.FileMode { return i.FileInfo.Mode() | fs.ModeSymlink }

func TestFingerprintLateMetadataFailures(t *testing.T) {
	for _, where := range []string{"entry-info-error", "unsafe-entry"} {
		t.Run(where, func(t *testing.T) {
			restoreSourceIO(t)
			p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", false)
			wantFP, wantFresh, err := Fingerprint(p)
			if err != nil || wantFP == "" || wantFresh.IsZero() {
				t.Fatal("synthetic_native_metadata_preflight_failed")
			}
			walk, info := sourceWalkDir, sourceEntryInfo
			walks, faults := 0, 0
			validationFinished := false
			sourceWalkDir = func(path string, fn fs.WalkDirFunc) error {
				walks++
				err := walk(path, fn)
				if walks == 1 {
					if err != nil {
						t.Fatal("synthetic_validation_failed_before_fault")
					}
					validationFinished = true
				}
				return err
			}
			sourceEntryInfo = func(e fs.DirEntry) (fs.FileInfo, error) {
				fi, err := info(e)
				if err != nil {
					t.Fatal("synthetic_native_entry_info_failed")
				}
				if validationFinished && e.Name() == "CURRENT" {
					if walks != 2 {
						t.Fatal("metadata_fault_not_in_fingerprint_walk")
					}
					faults++
					if where == "entry-info-error" {
						return nil, &fs.PathError{Op: "synthetic-private-operation", Path: "synthetic-private-path", Err: fs.ErrPermission}
					}
					// Preserve the real native Sys metadata; only the late entry's link mode changes.
					return lateUnsafeInfo{fi}, nil
				}
				return fi, nil
			}
			fp, fresh, err := Fingerprint(p)
			wantError := "outlookowa: unsafe_source"
			if where == "entry-info-error" {
				wantError = "outlookowa: source_unreadable"
			}
			if !validationFinished || walks != 2 || faults != 1 || err == nil || err.Error() != wantError || fp != "" || !fresh.IsZero() {
				t.Fatal("late_metadata_fault_returned_partial_or_wrong_phase")
			}
			if where == "entry-info-error" && !errors.Is(err, fs.ErrPermission) {
				t.Fatal("late_metadata_error_cause_lost")
			}
			sourceWalkDir, sourceEntryInfo = walk, info
			if fp, fresh, err := Fingerprint(p); err != nil || fp != wantFP || !fresh.Equal(wantFresh) {
				t.Fatal("restored_native_metadata_could_not_retry")
			}
		})
	}
}
