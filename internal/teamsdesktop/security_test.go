package teamsdesktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrivateSnapshotConsumerCannotOverwriteExistingCopy(t *testing.T) {
	root, err := NewPrivateSnapshotRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "index.db")
	t.Cleanup(func() {
		_ = os.Remove(path)
		_ = os.Remove(root)
	})
	file, err := CreatePrivateSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("private snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := CreatePrivateSnapshotFile(path); err == nil {
		_ = duplicate.Close()
		t.Fatal("consumer overwrote an existing snapshot")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "private snapshot" {
		t.Fatalf("existing copy not preserved: %v", err)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{root: 0o700, path: 0o600} {
			info, err := os.Stat(name)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("private permissions: %v", err)
			}
		}
	}
}
