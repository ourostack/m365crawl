//go:build darwin

package outlookdesktop

import (
	"path/filepath"
	"testing"
)

func TestDefaultRootIsTheOfficeGroupContainer(t *testing.T) {
	orig := userHome
	t.Cleanup(func() { userHome = orig })
	userHome = func() (string, error) { return "/fake/home", nil }
	got, err := DefaultRoot()
	want := filepath.Join("/fake/home", "Library", "Group Containers", "UBF8T346G9.Office", "Outlook", "Outlook 15 Profiles")
	if err != nil || got != want {
		t.Fatalf("got %q, %v want %q", got, err, want)
	}
}
