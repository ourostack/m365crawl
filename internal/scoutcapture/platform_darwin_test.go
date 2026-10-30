//go:build darwin

package scoutcapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

func TestDatalessFilesAreNeverOpened(t *testing.T) {
	root := appFolder(t)
	swap(t, &fstatat, func(dir int, name string, st *unix.Stat_t, flags int) error {
		err := unix.Fstatat(dir, name, st, flags)
		if name == "a-1.json" {
			st.Flags |= unix.SF_DATALESS
		}
		return err
	})
	swap(t, &openat, onSessionFile(unix.Openat, func() (int, error) {
		t.Fatal("a cloud-only file was opened")
		return -1, nil
	}))
	_, files, err := collect(t, Options{Root: root, MaxBytes: 100})
	if err != nil || files[0].State != StateCloudOnly || files[0].Reason != "scout_file_cloud_only" || files[0].Size == 0 || files[1].State != StateRead {
		t.Fatalf("%+v %v", files, err)
	}
}

func TestMaterializationRefusalsAreCloudOnly(t *testing.T) {
	check := func(name string, install func(t *testing.T)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if f := fileFault(t, 100, func() { install(t) }); f.State != StateCloudOnly || f.Reason != "scout_file_cloud_only" || f.Data != nil {
				t.Fatalf("%+v", f)
			}
		})
	}
	check("open", func(t *testing.T) {
		swap(t, &openat, onSessionFile(unix.Openat, func() (int, error) { return -1, unix.EDEADLK }))
	})
	check("read", func(t *testing.T) {
		swap(t, &readAll, func(io.Reader) ([]byte, error) { return nil, unix.EDEADLK })
	})
	check("became dataless", func(t *testing.T) {
		swap(t, &fstat, func(fd int, st *unix.Stat_t) error {
			err := unix.Fstat(fd, st)
			st.Flags |= unix.SF_DATALESS
			return err
		})
	})
}

func TestOnlyLocalAPFSQualifies(t *testing.T) {
	root := appFolder(t)
	swap(t, &fstatfs, func(fd int, fs *unix.Statfs_t) error {
		err := unix.Fstatfs(fd, fs)
		copy(fs.Fstypename[:], "smbfs\x00")
		return err
	})
	if _, _, err := collect(t, Options{Root: root, MaxBytes: 100}); err == nil || err.Error() != "scout_root_unqualified" {
		t.Fatalf("%v", err)
	}
}

func TestDiscoverFindsAppFoldersByLayout(t *testing.T) {
	home := t.TempDir()
	cloud := filepath.Join(home, "Library", "CloudStorage")
	for _, dir := range []string{"OneDrive-Synthetic/Apps/Scout App/sessions/devices", "OneDrive-Synthetic/Apps/Renamed Scout/sessions/devices",
		"OneDrive-Synthetic/Apps/Other App/data", "OneDrive-Personal/Apps/Scout/sessions", "OneDrive-SharedLibraries-Org/Apps/Scout/sessions/devices",
		"Dropbox/Apps/Scout/sessions/devices", "OneDrive-Empty"} {
		if err := os.MkdirAll(filepath.Join(cloud, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(cloud, "OneDrive-File", "Apps"), "not a folder")
	found, err := Discover(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(filepath.Join(cloud, "OneDrive-Synthetic", "Apps"))
	want := []string{filepath.Join(resolved, "Renamed Scout"), filepath.Join(resolved, "Scout App")}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("found %v want %v", found, want)
	}
	if found, err := Discover(context.Background(), filepath.Join(home, "missing")); err != nil || len(found) != 0 {
		t.Fatalf("missing home: %v %v", found, err)
	}
	// An Apps folder that cannot be listed contributes nothing.
	calls := 0
	swap(t, &readNames, func(dir *os.File) ([]string, error) {
		if calls++; calls > 1 {
			return nil, unix.EIO
		}
		return dir.Readdirnames(-1)
	})
	if found, err := Discover(context.Background(), home); err != nil || len(found) != 0 {
		t.Fatalf("unreadable: %v %v", found, err)
	}
	// Cancellation between OneDrive folders.
	ctx, cancel := context.WithCancel(context.Background())
	swap(t, &readNames, func(dir *os.File) ([]string, error) {
		cancel()
		return dir.Readdirnames(-1)
	})
	if _, err := Discover(ctx, home); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestGuardFailures(t *testing.T) {
	root := appFolder(t)
	policyOnce.Do(loadPolicy)
	realGet, realSet := policyGet, policySet
	code := func() string {
		_, _, err := collect(t, Options{Root: root, MaxBytes: 100})
		var coded *Error
		if errors.As(err, &coded) {
			return coded.Code
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	swap(t, &policyGet, func(int32, int32) int32 { return -1 })
	if c := code(); c != "scout_guard_unavailable" {
		t.Fatalf("get: %s", c)
	}
	swap(t, &policyGet, realGet)
	swap(t, &policySet, func(int32, int32, int32) int32 { return 1 })
	if c := code(); c != "scout_guard_unavailable\nscout_guard_restore_failed" && c != "scout_guard_unavailable" {
		t.Fatalf("set: %s", c)
	}
	swap(t, &policySet, realSet)
	sets := 0
	swap(t, &policySet, func(kind, scope, value int32) int32 {
		if sets++; sets == 2 {
			return 1
		}
		return realSet(kind, scope, value)
	})
	if _, _, err := collect(t, Options{Root: root, MaxBytes: 100}); !containsCode(err, "scout_guard_restore_failed") {
		t.Fatalf("restore: %v", err)
	}
	swap(t, &policySet, realSet)
	if err := withGuard(context.Background(), func() error { panic("synthetic") }); !containsCode(err, "scout_capture_failed") {
		t.Fatalf("panic: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := withGuard(ctx, func() error { t.Fatal("ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	swap(t, &policyErr, errors.New("synthetic"))
	if c := code(); c != "scout_guard_unavailable" {
		t.Fatalf("load: %s", c)
	}
}

func containsCode(err error, code string) bool {
	var coded *Error
	if errors.As(err, &coded) && coded.Code == code {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			if containsCode(inner, code) {
				return true
			}
		}
	}
	return false
}

func TestLoadPolicyFailures(t *testing.T) {
	swap(t, &policyErr, nil)
	swap(t, &policyOpen, func(string, int) (uintptr, error) { return 0, errors.New("synthetic") })
	loadPolicy()
	if policyErr == nil {
		t.Fatal("open failure ignored")
	}
	policyErr = nil
	swap(t, &policyOpen, purego.Dlopen)
	swap(t, &policySymbol, func(uintptr, string) (uintptr, error) { return 0, errors.New("synthetic") })
	closed := false
	swap(t, &policyClose, func(uintptr) error { closed = true; return nil })
	loadPolicy()
	if policyErr == nil || !closed {
		t.Fatal("symbol failure ignored")
	}
}
