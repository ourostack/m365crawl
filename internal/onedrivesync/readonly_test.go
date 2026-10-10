package onedrivesync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadIndexSafeSourceAndOpenFailures(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows)
	for _, source := range []string{path + "-missing", filepath.Dir(path)} {
		got, err := ReadIndex(context.Background(), source)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), source) {
			t.Fatalf("unsafe missing/nonfile source: %v", err)
		}
	}
	originalOpen, originalAbs := openIndex, indexAbs
	t.Cleanup(func() { openIndex, indexAbs = originalOpen, originalAbs })
	openIndex = func(string, string) (*sql.DB, error) { return nil, errInjected }
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-sync-payload") {
		t.Fatalf("open failure exposed source: %v", err)
	}
	openIndex = originalOpen
	indexAbs = func(string) (string, error) { return "", errInjected }
	got, err = ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-sync-payload") {
		t.Fatalf("absolute path failure exposed source: %v", err)
	}
}

func TestReadIndexOpenFailureCancellationIdentity(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := openIndex
	t.Cleanup(func() { openIndex = original })
	openIndex = func(string, string) (*sql.DB, error) {
		cancel()
		return nil, errInjected
	}
	got, err := ReadIndex(ctx, path)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("open-boundary cancellation lost: %v", err)
	}
}

func TestReadIndexEscapedSourceBytesUnchanged(t *testing.T) {
	source := fixture(t, nativeSchema, nativeRows)
	path := filepath.Join(filepath.Dir(source), "private # percent% source.db")
	if err := os.Rename(source, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path) // #nosec G304 -- exact test-owned synthetic fixture, never an app path.
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Files) != 1 {
		t.Fatalf("escaped private-copy URI failed: %v", err)
	}
	after, err := os.ReadFile(path) // #nosec G304 -- same exact test-owned synthetic fixture.
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatalf("supplied source bytes changed: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read created source companion %s: %v", suffix, err)
		}
	}
}

func TestReadIndexCancellationPrecedesRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := fixture(t, nativeSchema, nativeRows)
	got, err := readIndex(ctx, path, readLimits{})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("pre-canceled input lost cancellation identity: %v", err)
	}
}
