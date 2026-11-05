//go:build darwin

package fileprivacy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestPrivateImageDarwinInheritedACLRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/chmod", "+a", "group:staff allow read,file_inherit,only_inherit", dir).CombinedOutput(); err != nil {
		t.Fatal("controlled native inherited ACL fixture failed", err, string(output))
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	var coded *errs.Coded
	if err := Parent(parent); !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
		t.Error("mode-safe parent with foreign file-inheritable ACL admitted", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "inherited.png"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("native fixture must retain0600 despite ACL inheritance", info, err)
	}
	coded = nil
	if err := Temporary(file); !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
		t.Error("mode0600 child with inherited foreign read ACL admitted", err)
	}
}
