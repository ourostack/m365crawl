//go:build windows

package officecapture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTextWindowsEvidencePreservesCompleteNativeIDs(t *testing.T) {
	path := textFixture(t)
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info := inputWinInfo
	t.Cleanup(func() { inputWinInfo = info })
	inputWinInfo = func(_ windows.Handle, st *windows.ByHandleFileInformation) error {
		st.VolumeSerialNumber = 9
		st.FileIndexHigh = 0x200000
		st.FileIndexLow = 1
		st.CreationTime.LowDateTime = 7
		st.LastWriteTime.LowDateTime = 0
		st.FileSizeLow = 8
		return nil
	}
	// No source is opened: this fixture verifies conversion from held-handle native fields.
	e, err := heldTextEvidence(file)
	if err != nil || e.FileID != "9007199254740993" || e.CreationRaw == nil || *e.CreationRaw != "7" ||
		e.NativeGenerationRaw != nil || e.ModificationRaw == nil || *e.ModificationRaw != "0" || e.ModifiedAt != nil ||
		!e.GenerationQualified || e.GenerationKind != "native_creation" {
		t.Fatalf("native encoding: %#v %v", e, err)
	}
	inputWinInfo = func(windows.Handle, *windows.ByHandleFileInformation) error { return os.ErrPermission }
	if _, err := heldTextEvidence(file); err == nil {
		t.Fatal("failed held native info")
	}
	inputWinInfo = func(_ windows.Handle, st *windows.ByHandleFileInformation) error { st.FileSizeHigh = 1; return nil }
	if _, err := heldTextEvidence(file); !errors.As(err, new(*ReadError)) {
		t.Fatal("native size limit")
	}
	inputWinInfo = func(_ windows.Handle, st *windows.ByHandleFileInformation) error {
		st.VolumeSerialNumber = 9
		st.FileIndexLow = 7
		st.CreationTime.LowDateTime = 7
		st.LastWriteTime.HighDateTime = ^uint32(0)
		st.LastWriteTime.LowDateTime = ^uint32(0)
		return nil
	}
	if e, err := heldTextEvidence(file); err != nil || e.ModifiedAt != nil || e.ModificationRaw == nil || *e.ModificationRaw != "18446744073709551615" {
		t.Fatal("unsupported parsed native clock lost its raw evidence")
	}
}
