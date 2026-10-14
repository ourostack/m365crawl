//go:build windows

package officecapture

import (
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
}
