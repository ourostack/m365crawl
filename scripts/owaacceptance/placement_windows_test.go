package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
	"golang.org/x/sys/windows"
)

func TestPlacementRefusesSourceStorageBeforeWrites(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"source_scratch", "scratch_descendant", "receipt_source", "receipt_descendant", "case_variant"} {
		t.Run(mode, func(t *testing.T) {
			source := qualifiedFixtureRoot(t)
			scratch := t.TempDir()
			receipt := filepath.Join(t.TempDir(), "receipt")
			switch mode {
			case "source_scratch":
				scratch = source
			case "scratch_descendant":
				scratch = filepath.Join(source, "Default")
			case "receipt_source":
				receipt = source
			case "receipt_descendant":
				receipt = filepath.Join(source, "supervisor-records")
			case "case_variant":
				scratch = strings.ToUpper(source)
			}
			before := placementSourceEvidence(t, source)
			_, err := runQualified(binary, testDigest(t, binary), source, scratch, receipt, 30*time.Second)
			if err == nil {
				t.Error("source_storage_overlap_qualified")
			} else if err.Error() != "qualification_storage_placement_refused" {
				t.Error("source_overlap_not_preflight_refused")
			}
			after := placementSourceEvidence(t, source)
			if before != after {
				t.Error("readonly_source_changed_by_supervisor")
			}
			if _, err := os.Lstat(filepath.Join(receipt, protocol.OwnerName)); !errors.Is(err, os.ErrNotExist) {
				t.Error("overlap_preflight_wrote_ownership")
			}
		})
	}
}

// Compare names, modes, ACLs and fixture byte hashes, never dump source payload. Volatile timestamps are not a source-write oracle.
func placementSourceEvidence(t *testing.T, source string) string {
	t.Helper()
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal("synthetic_source_root_failed")
	}
	defer func() {
		if root.Close() != nil {
			t.Error("synthetic_source_root_close_failed")
		}
	}()
	var evidence strings.Builder
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		evidence.WriteString(path)
		evidence.WriteString(info.Mode().String())
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		evidence.WriteString(sd.String())
		if !entry.IsDir() {
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			data, err := root.ReadFile(relative)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			evidence.WriteString(hex.EncodeToString(sum[:]))
		}
		return nil
	})
	if err != nil {
		t.Fatal("synthetic_source_inventory_failed")
	}
	return evidence.String()
}

func TestPlacementAllowsCommonParentAndReceiptScratchLayouts(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}

	for _, mode := range []string{"siblings", "source_under_scratch_parent", "source_under_receipt", "scratch_under_receipt", "receipt_under_scratch"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			if mode == "source_under_receipt" && protocol.MakePrivateDirectory(base) != nil {
				t.Fatal("private_receipt_parent_failed")
			}
			source := filepath.Join(base, "source")
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(source, "Default"))
			scratch := filepath.Join(base, "scratch")
			receipt := filepath.Join(base, "receipt")
			switch mode {
			case "source_under_scratch_parent":
				scratch = base
			case "source_under_receipt":
				receipt = base
			case "scratch_under_receipt":
				if os.Mkdir(receipt, 0o700) != nil || protocol.MakePrivateDirectory(receipt) != nil {
					t.Fatal("private_receipt_parent_failed")
				}
				scratch = filepath.Join(receipt, "scratch")
			case "receipt_under_scratch":
				receipt = filepath.Join(scratch, "receipt")
			}
			if scratch != base && os.Mkdir(scratch, 0o700) != nil {
				t.Fatal("synthetic_scratch_failed")
			}
			before := placementSourceEvidence(t, source)
			witness, err := runQualified(binary, testDigest(t, binary), source, scratch, receipt, 30*time.Second)
			if err != nil || witness.Observed != 1 || witness.Counts["messages"] != 5 {
				t.Fatal("disjoint_source_storage_refused")
			}
			if before != placementSourceEvidence(t, source) {
				t.Fatal("positive_source_changed_by_supervisor")
			}
			data, err := protocol.ReadProtected(filepath.Join(receipt, protocol.CloseoutName))
			if err != nil {
				t.Fatal("positive_placement_closeout_missing")
			}
			closeout, err := protocol.DecodeCloseout(data)
			if err != nil || !closeout.Qualified || closeout.RootState != "absent" || !closeout.ConsumersReleased || !closeout.NativeCloseVerified {
				t.Fatal("positive_placement_closeout_unproved")
			}
		})
	}
}

func TestPlacementProducerRejectsNativeAliasesBeforeWrites(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"short_scratch", "volume_receipt"} {
		t.Run(mode, func(t *testing.T) {
			source := qualifiedFixtureRoot(t)
			name, err := windows.UTF16PtrFromString(source)
			if err != nil {
				t.Fatal("synthetic_alias_path_failed")
			}
			buffer := make([]uint16, 32768)
			var alias string
			if mode == "short_scratch" {
				length, err := windows.GetShortPathName(name, &buffer[0], 32768)
				if err != nil || length == 0 || length >= 32768 {
					t.Fatal("synthetic_short_alias_failed")
				}
				alias = windows.UTF16ToString(buffer[:length])
				if strings.EqualFold(alias, source) {
					t.Skip("filesystem did not provide a distinct short-name alias")
				}
			} else {
				mount := filepath.VolumeName(source) + `\`
				volumePath, err := windows.UTF16PtrFromString(mount)
				if err != nil || windows.GetVolumeNameForVolumeMountPoint(volumePath, &buffer[0], 32768) != nil {
					t.Fatal("synthetic_volume_alias_failed")
				}
				relative, err := filepath.Rel(mount, source)
				if err != nil {
					t.Fatal("synthetic_volume_relative_failed")
				}
				alias = filepath.Join(windows.UTF16ToString(buffer), relative)
			}
			parent, receipt := t.TempDir(), filepath.Join(t.TempDir(), "receipt")
			if mode == "short_scratch" {
				parent = alias
			} else {
				receipt = filepath.Join(alias, "receipt")
			}
			before := placementSourceEvidence(t, source)
			if _, err := runQualified(binary, testDigest(t, binary), source, parent, receipt, 30*time.Second); err == nil || err.Error() != "qualification_storage_placement_refused" {
				t.Error("native_alias_overlap_not_preflight_refused")
			}
			if before != placementSourceEvidence(t, source) {
				t.Error("native_alias_source_changed")
			}
			if _, err := os.Lstat(filepath.Join(receipt, protocol.OwnerName)); !errors.Is(err, os.ErrNotExist) {
				t.Error("native_alias_preflight_wrote_ownership")
			}
		})
	}
}
func TestPlacementChildGuardRefusesCoherentSourceOverlap(t *testing.T) {
	binary := os.Getenv("OWA_ACCEPTANCE_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit compiled synthetic acceptance artifact required")
	}
	for _, mode := range []string{"source_temp", "source_ancestor_temp", "source_ancestor_receipt"} {
		t.Run(mode, func(t *testing.T) {
			w, q, err := newQualifiedRun(binary, testDigest(t, binary), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
			if err != nil {
				t.Fatal("synthetic_guard_workspace_failed")
			}
			source := w.dir
			switch mode {
			case "source_ancestor_temp":
				source = filepath.Dir(w.dir)
			case "source_ancestor_receipt":
				source = filepath.Dir(q.receipt)
			}
			copySyntheticFixture(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(source, "Default"))
			// Coherent test-only records exercise the consumer independently of producer preflight. Only this copied committed fixture is the child's explicit source.
			q.owner.Source = source
			data, err := protocol.Encode(q.owner)
			if err != nil {
				t.Fatal("synthetic_owner_encode_failed")
			}
			// #nosec G703 -- Exact test-owned protected initial record before the guarded copied-synthetic child starts.
			if os.WriteFile(filepath.Join(q.receipt, protocol.OwnerName), data, 0o600) != nil {
				t.Fatal("synthetic_owner_mutation_failed")
			}
			exit, result := w.run(binary, []string{"-test.run=^" + protocol.TestName + "$", "-test.timeout=4m"}, qualifiedEnv(os.Environ(), q), 30*time.Second)
			err = q.finish(exit, result)
			if err == nil || exit == 0 {
				t.Error("child_guard_source_overlap_accepted")
			}
			if _, err := os.Lstat(filepath.Join(q.receipt, protocol.WitnessName)); !errors.Is(err, os.ErrNotExist) {
				t.Error("overlapping_child_produced_witness")
			}
			if !q.consumersReleased || !q.cleanupVerified {
				t.Fatal("synthetic_guard_release_unproved")
			}
			closeData, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CloseoutName))
			if err != nil {
				t.Fatal("synthetic_guard_closeout_missing")
			}
			closeout, err := protocol.DecodeCloseout(closeData)
			if err != nil || closeout.Qualified || closeout.RootState != "absent" || !closeout.ConsumersReleased || !closeout.NativeCloseVerified {
				t.Fatal("synthetic_guard_closeout_unproved")
			}
			if _, err := os.Lstat(w.dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("synthetic_guard_payload_retained")
			}
		})
	}
}
