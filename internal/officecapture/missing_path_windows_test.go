//go:build windows

package officecapture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsMissingOrdinaryDirectoryIsRecognizableAbsence(t *testing.T) {
	path, _ := ordinaryFixture(t)
	input, err := openInput(filepath.Join(filepath.Dir(path), "missing"), true)
	if input != nil {
		if err := input.close(); err != nil {
			t.Fatal("unexpected missing-path handle did not close")
		}
		t.Fatal("missing ordinary directory unexpectedly opened")
	}
	var status windows.NTStatus
	if errors.As(err, &status) {
		t.Logf("native_status=0x%08x dos_errno=%d recognized_absence=%t",
			uint32(status), status.Errno(), errors.Is(status.Errno(), os.ErrNotExist))
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing ordinary directory was not exposed as recognizable absence")
	}
}
