package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStoragePlacementNativeComponents(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	sibling := filepath.Join(base, "source-sibling")
	child := filepath.Join(source, "nested")
	for _, path := range []string{source, sibling, child} {
		if os.Mkdir(path, 0o700) != nil {
			t.Fatal("synthetic_directory_failed")
		}
	}
	for _, path := range []string{source, child, filepath.Join(child, "new-receipt"), strings.ToUpper(source), strings.ToUpper(child)} {
		if StorageOutsideSource(source, path) == nil {
			t.Error("native_source_ancestor_accepted")
		}
	}
	for _, path := range []string{sibling, filepath.Join(sibling, "new-receipt"), base} {
		if StorageOutsideSource(source, path) != nil {
			t.Error("native_common_ancestor_or_prefix_sibling_refused")
		}
	}
	for _, path := range []string{"relative", filepath.Join(base, "absent", "nested")} {
		if StorageOutsideSource(source, path) == nil {
			t.Error("unproved_placement_accepted")
		}
	}
}

func TestStoragePlacementShortNameAlias(t *testing.T) {
	source := filepath.Join(t.TempDir(), "long synthetic source name")
	if os.Mkdir(source, 0o700) != nil {
		t.Fatal("synthetic_directory_failed")
	}
	name, err := windows.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal("synthetic_path_failed")
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetShortPathName(name, &buffer[0], 32768)
	if err != nil || length == 0 || length >= 32768 {
		t.Fatal("native_short_alias_lookup_failed")
	}
	alias := windows.UTF16ToString(buffer[:length])
	if strings.EqualFold(source, alias) {
		t.Skip("filesystem did not provide a distinct short-name alias")
	}
	a, err := NativeIdentity(source, true)
	b, aliasErr := NativeIdentity(alias, true)
	if err != nil || aliasErr != nil || a != b {
		t.Fatal("short_alias_identity_unproved")
	}
	if StorageOutsideSource(source, filepath.Join(alias, "new-receipt")) == nil || StorageOutsideSource(alias, source) == nil {
		t.Fatal("short_alias_source_overlap_accepted")
	}
}

func TestStoragePlacementVolumeAliasBoundary(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	sibling := filepath.Join(base, "source-sibling")
	if os.Mkdir(source, 0o700) != nil || os.Mkdir(sibling, 0o700) != nil {
		t.Fatal("synthetic_directory_failed")
	}
	mount := filepath.VolumeName(base) + `\`
	name, err := windows.UTF16PtrFromString(mount)
	if err != nil {
		t.Fatal("synthetic_volume_path_failed")
	}
	buffer := make([]uint16, 128)
	if windows.GetVolumeNameForVolumeMountPoint(name, &buffer[0], 128) != nil {
		t.Fatal("native_volume_alias_lookup_failed")
	}
	volume := windows.UTF16ToString(buffer)
	relative, err := filepath.Rel(mount, source)
	if err != nil {
		t.Fatal("synthetic_volume_relative_failed")
	}
	alias := filepath.Join(volume, relative)
	a, err := NativeIdentity(source, true)
	b, aliasErr := NativeIdentity(alias, true)
	if err != nil || aliasErr != nil || a != b {
		t.Fatal("volume_alias_identity_unproved")
	}
	if StorageOutsideSource(source, filepath.Join(alias, "new-receipt")) == nil || StorageOutsideSource(alias, source) == nil {
		t.Fatal("volume_alias_source_overlap_accepted")
	}
	if StorageOutsideSource(alias, sibling) != nil {
		t.Fatal("volume_alias_sibling_refused")
	}
}
