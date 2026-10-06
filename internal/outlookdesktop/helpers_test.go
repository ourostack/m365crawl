package outlookdesktop

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tempHome points the temp directory at a fresh directory under t.TempDir and returns it, so a
// test can assert that no snapshot directory is left behind.
func tempHome(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("TMPDIR", d)
	t.Setenv("TMP", d)
	t.Setenv("TEMP", d)
	return d
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// syntheticStore returns invented bytes (the word FIXTURE repeated) of the given length.
func syntheticStore(n int) []byte { return bytes.Repeat([]byte("FIXTURE"), n/7+1)[:n] }

// fakeProfile creates <root>/<name>/HxStore.hxd and its synthetic sibling files, and returns
// the store path.
func fakeProfile(t *testing.T, root, name string, size int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	store := filepath.Join(dir, StoreFileName)
	mustWrite(t, store, syntheticStore(size))
	mustWrite(t, filepath.Join(dir, "HxStore.lock"), []byte("lock"))
	mustWrite(t, filepath.Join(dir, "hxcore.hfl"), []byte("hfl"))
	mustWrite(t, filepath.Join(dir, "Files", "S0", "2", "EFMData", "1.dat"), []byte("efm"))
	return store
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func assertClean(t *testing.T, tmp string) {
	t.Helper()
	if got := entries(t, tmp); len(got) != 0 {
		t.Errorf("temp directory not clean: %v", got)
	}
}

func setMtime(t *testing.T, path string, d time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	m := info.ModTime().Add(d)
	if err := os.Chtimes(path, m, m); err != nil {
		t.Fatal(err)
	}
}
