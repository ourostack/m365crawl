package snapshotowner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotOwnerFreshMarkerFailureClosesFile(t *testing.T) {
	own, encode, protected := ownerOwnMarker, ownerEncodeMarker, ownerProtected
	t.Cleanup(func() {
		ownerOwnMarker, ownerEncodeMarker, ownerProtected = own, encode, protected
	})
	ownerProtected = func(string, fs.FileInfo) bool { return true }
	fault := errors.New("synthetic fresh-marker ownership refusal")
	var captured *os.File
	ownerOwnMarker = func(f *os.File) error { captured = f; return fault }
	ownerEncodeMarker = func(*os.File, marker) error {
		t.Fatal("failed ownership must not encode an ownership claim")
		return nil
	}
	dir := t.TempDir()
	if err := Register(dir); !errors.Is(err, fault) {
		t.Fatalf("lost ownership error: %v", err)
	}
	if captured == nil {
		t.Fatal("did not reach the exclusive fresh-marker boundary")
	}
	if n, err := captured.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed fresh marker close oracle: wrote %d bytes, err=%v", n, err)
	}
	info, err := os.Stat(filepath.Join(dir, markerName))
	if err != nil || info.Size() != 0 {
		t.Fatalf("failed ownership encoded marker contents: %v %v", info, err)
	}
}

func TestSnapshotOwnerDoesNotOwnRefusedOrExistingMarkers(t *testing.T) {
	own, protected := ownerOwnMarker, ownerProtected
	t.Cleanup(func() { ownerOwnMarker, ownerProtected = own, protected })
	calls := 0
	ownerOwnMarker = func(*os.File) error { calls++; return nil }
	dir := t.TempDir()
	ownerProtected = func(string, fs.FileInfo) bool { return false }
	if err := Register(dir); err == nil || calls != 0 {
		t.Fatalf("unqualified root reached an ownership mutation: %v calls=%d", err, calls)
	}
	if _, err := os.Lstat(filepath.Join(dir, markerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unqualified root acquired a marker: %v", err)
	}
	ownerProtected = func(string, fs.FileInfo) bool { return true }
	if err := Register(dir); err != nil || calls != 1 {
		t.Fatalf("fresh qualified marker: %v calls=%d", err, calls)
	}
	path := filepath.Join(dir, markerName)
	before, err := os.ReadFile(path) //nolint:gosec // G304: exact marker just created exclusively in this test's t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(dir); err == nil || calls != 1 {
		t.Fatalf("existing marker reached an ownership mutation: %v calls=%d", err, calls)
	}
	after, err := os.ReadFile(path) //nolint:gosec // G304: same owned marker, proving refusal preserved its bytes
	if err != nil || string(after) != string(before) {
		t.Fatalf("refusal changed existing marker: %v", err)
	}
}
