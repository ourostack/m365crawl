package scoutcapture

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	devA = "0123456789abcdef0123456789abcdef"
	devB = "fedcba9876543210fedcba9876543210"
)

func swap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// appFolder builds a synthetic Scout app folder: two device folders, index files beside the
// sessions and one folder that is not a device.
func appFolder(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	devices := filepath.Join(root, "sessions", "devices")
	write(t, filepath.Join(root, "sessions", "sessions-index.json"), `{}`)
	write(t, filepath.Join(devices, devA, "b-2.json"), `{"id":"b-2"}`)
	write(t, filepath.Join(devices, devA, "a-1.json"), `{"id":"a-1"}`)
	write(t, filepath.Join(devices, devA, "sessions-index.json"), `{}`)
	write(t, filepath.Join(devices, devA, "skip-list.json"), `{}`)
	write(t, filepath.Join(devices, devB, "c-3.json"), `{"id":"c-3"}`)
	if err := os.MkdirAll(filepath.Join(devices, "not-a-device"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
