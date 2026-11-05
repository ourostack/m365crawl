//go:build windows

package cli

import (
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/fileprivacy"
	"golang.org/x/sys/windows"
)

func TestImageWindowsOutputFixturePrivacy(t *testing.T) {
	_ = newEnv(t)
	for _, tc := range []struct {
		name, path string
		private    bool
	}{
		{"plain-temp-control", t.TempDir(), false},
		{"native-private-output", imageOutputDir(t), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, err := os.Open(tc.path) // #nosec G304 -- Exact owned synthetic temporary or native-private fixture.
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := parent.Close(); err != nil {
					t.Error(err)
				}
			}()
			sd, err := windows.GetSecurityInfo(windows.Handle(parent.Fd()), windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil || sd == nil {
				t.Fatal("fixture security descriptor unavailable", err)
			}
			control, _, err := sd.Control()
			if err != nil {
				t.Fatal(err)
			}
			protected := control&windows.SE_DACL_PROTECTED != 0
			privacyErr := fileprivacy.Parent(parent)
			t.Logf("protected_dacl=%t admitted=%t", protected, privacyErr == nil)
			if tc.private && (!protected || privacyErr != nil) {
				t.Fatal("native fixture does not establish private output admission", privacyErr)
			}
		})
	}
}
