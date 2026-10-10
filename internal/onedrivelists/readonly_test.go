package onedrivelists

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestReadIndexEscapedReadOnlyCopy(t *testing.T) {
	path := fixture(t)
	name := "lists # % space.db"
	if runtime.GOOS != "windows" {
		name = "lists # % ? space.db"
	}
	escaped := filepath.Join(filepath.Dir(path), name)
	if err := os.Rename(path, escaped); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(escaped) // #nosec G304 -- synthetic fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(context.Background(), escaped)
	if err != nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("escaped immutable source=%#v,%v", got, err)
	}
	after, err := os.ReadFile(escaped) // #nosec G304 -- same synthetic fixture.
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed source bytes")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(escaped + suffix); !os.IsNotExist(err) {
			t.Fatalf("reader created %s", suffix)
		}
	}
}
