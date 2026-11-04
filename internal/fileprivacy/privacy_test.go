package fileprivacy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateImageHandleRefusal(t *testing.T) {
	if err := Parent(nil); err == nil {
		t.Fatal("missing parent handle admitted")
	}
	if err := Temporary(nil); err == nil {
		t.Fatal("missing temporary handle admitted")
	}
	path := filepath.Join(t.TempDir(), "closed")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Temporary(file); err == nil {
		t.Fatal("closed temporary handle admitted")
	}
}
