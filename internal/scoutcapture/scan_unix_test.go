//go:build darwin || linux

package scoutcapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

func collect(t *testing.T, opts Options) (Result, []File, error) {
	t.Helper()
	var files []File
	res, err := Scan(context.Background(), opts, func(f File) error {
		files = append(files, f)
		return nil
	})
	return res, files, err
}

func TestScanReadsEveryResidentSession(t *testing.T) {
	if !Supported() {
		t.Fatal("unsupported")
	}
	root := appFolder(t)
	res, files, err := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != root || res.Ignored != 3 || len(res.Devices) != 2 || !res.Devices[0].Listed || res.Devices[0].ID != devA || res.Devices[1].ID != devB {
		t.Fatalf("result: %+v", res)
	}
	var got []string
	for _, f := range files {
		if f.State != StateRead || f.Reason != "" || f.Size != int64(len(f.Data)) || f.Modified.IsZero() {
			t.Fatalf("file: %+v", f)
		}
		got = append(got, f.Device[:4]+"/"+f.Name+"="+string(f.Data))
	}
	want := []string{"0123/a-1.json={\"id\":\"a-1\"}", "0123/b-2.json={\"id\":\"b-2\"}", "fedc/c-3.json={\"id\":\"c-3\"}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	// A second scan that knows a file's size and time does not open it.
	known := map[Key]Known{{devA, "a-1.json"}: {Size: files[0].Size, ModNanos: files[0].Modified.UnixNano()}, {devB, "c-3.json"}: {Size: 1}}
	opened := 0
	swap(t, &openat, func(dir int, name string, flags int, mode uint32) (int, error) {
		if !strings.HasSuffix(name, ".json") || flags&unix.O_DIRECTORY != 0 {
			return unix.Openat(dir, name, flags, mode)
		}
		opened++
		return unix.Openat(dir, name, flags, mode)
	})
	_, files, err = collect(t, Options{Root: root, MaxBytes: 1 << 20, Known: known})
	if err != nil || files[0].State != StateUnchanged || files[0].Data != nil || files[1].State != StateRead || files[2].State != StateRead || opened != 2 {
		t.Fatalf("incremental: %+v %v opened=%d", files, err, opened)
	}
}

func TestScanRefusesRootsThatAreNotAppFolders(t *testing.T) {
	root := appFolder(t)
	file := filepath.Join(root, "sessions", "sessions-index.json")
	noDevices := t.TempDir()
	write(t, filepath.Join(noDevices, "sessions", "x"), "")
	for _, dir := range []string{"relative", root + "/", filepath.Join(root, "missing"), file, filepath.Join(root, "sessions", "devices", devA), noDevices} {
		_, _, err := collect(t, Options{Root: dir, MaxBytes: 10})
		var coded *Error
		if !errors.As(err, &coded) || coded.Code != "scout_root_invalid" {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}

func TestScanReportsUnreadableFolders(t *testing.T) {
	root := appFolder(t)
	calls := 0
	swap(t, &readNames, func(dir *os.File) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, unix.EIO
		}
		return dir.Readdirnames(-1)
	})
	_, _, err := collect(t, Options{Root: root, MaxBytes: 10})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != "scout_devices_unreadable" {
		t.Fatalf("devices: %v", err)
	}
	calls = 0
	swap(t, &readNames, func(dir *os.File) ([]string, error) {
		calls++
		if calls == 2 {
			return nil, unix.EIO
		}
		return dir.Readdirnames(-1)
	})
	res, files, err := collect(t, Options{Root: root, MaxBytes: 10})
	if err != nil || res.Devices[0].Listed || res.Devices[0].Reason != "scout_device_unreadable" || !res.Devices[1].Listed || len(files) != 1 {
		t.Fatalf("device listing: %+v %v %v", res, files, err)
	}
	// A device folder that is a link is never followed.
	other := appFolder(t)
	devices := filepath.Join(other, "sessions", "devices")
	if err := os.RemoveAll(filepath.Join(devices, devB)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(devices, devA), filepath.Join(devices, devB)); err != nil {
		t.Fatal(err)
	}
	calls = -100
	res, files, err = collect(t, Options{Root: other, MaxBytes: 10})
	if err != nil || res.Devices[1].Listed || res.Devices[1].Reason != "scout_device_unreadable" || len(files) != 2 {
		t.Fatalf("linked device: %+v %v %v", res, files, err)
	}
}

func TestScanFileStates(t *testing.T) {
	root := appFolder(t)
	device := filepath.Join(root, "sessions", "devices", devA)
	if err := os.Symlink(filepath.Join(device, "a-1.json"), filepath.Join(device, "l-link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(device, "d-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(device, "z-big.json"), strings.Repeat("x", 64))
	_, files, err := collect(t, Options{Root: root, MaxBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, f := range files {
		states[f.Name] = f.State + ":" + f.Reason
		if f.State != StateRead && f.Data != nil {
			t.Fatalf("data kept for %s", f.Name)
		}
	}
	want := map[string]string{"a-1.json": "read:", "b-2.json": "read:", "c-3.json": "read:", "l-link.json": "unsafe:scout_file_unsafe",
		"d-dir.json": "unsafe:scout_file_unsafe", "z-big.json": "too_large:scout_file_too_large"}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states %v", states)
	}
}

// fileFault runs one scan of a one-file folder with a fault installed and returns that file.
func fileFault(t *testing.T, maxBytes int64, install func()) File {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "sessions", "devices", devA, "a-1.json"), `{"id":"a-1"}`)
	install()
	_, files, err := collect(t, Options{Root: root, MaxBytes: maxBytes})
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
	return files[0]
}

func onSessionFile(real func(int, string, int, uint32) (int, error), fault func() (int, error)) func(int, string, int, uint32) (int, error) {
	return func(dir int, name string, flags int, mode uint32) (int, error) {
		if name == "a-1.json" {
			return fault()
		}
		return real(dir, name, flags, mode)
	}
}

func TestScanFileFaults(t *testing.T) {
	check := func(name string, install func(t *testing.T), state, reason string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if f := fileFault(t, 100, func() { install(t) }); f.State != state || f.Reason != reason || f.Data != nil {
				t.Fatalf("%+v", f)
			}
		})
	}
	check("fstatat", func(t *testing.T) {
		swap(t, &fstatat, func(int, string, *unix.Stat_t, int) error { return unix.EIO })
	}, StateFailed, "scout_file_unreadable")
	check("open", func(t *testing.T) {
		swap(t, &openat, onSessionFile(unix.Openat, func() (int, error) { return -1, unix.EACCES }))
	}, StateFailed, "scout_file_unreadable")
	check("fstat", func(t *testing.T) {
		swap(t, &fstat, func(int, *unix.Stat_t) error { return unix.EIO })
	}, StateFailed, "scout_file_unreadable")
	check("replaced", func(t *testing.T) {
		swap(t, &fstat, func(fd int, st *unix.Stat_t) error {
			err := unix.Fstat(fd, st)
			st.Ino++
			return err
		})
	}, StateFailed, "scout_file_changed_during_read")
	check("read", func(t *testing.T) {
		swap(t, &readAll, func(io.Reader) ([]byte, error) { return nil, unix.EIO })
	}, StateFailed, "scout_file_unreadable")
	check("grew", func(t *testing.T) {
		swap(t, &readAll, func(io.Reader) ([]byte, error) { return make([]byte, 101), nil })
	}, StateTooLarge, "scout_file_too_large")
	check("short", func(t *testing.T) {
		swap(t, &readAll, func(io.Reader) ([]byte, error) { return []byte("{}"), nil })
	}, StateFailed, "scout_file_changed_during_read")
	calls := 0
	check("second fstat", func(t *testing.T) {
		swap(t, &fstat, func(fd int, st *unix.Stat_t) error {
			calls++
			if calls == 2 {
				return unix.EIO
			}
			return unix.Fstat(fd, st)
		})
	}, StateFailed, "scout_file_unreadable")
}

func TestScanStopsOnConsumerErrorAndCancellation(t *testing.T) {
	root := appFolder(t)
	stop := errors.New("stop")
	n := 0
	_, err := Scan(context.Background(), Options{Root: root, MaxBytes: 100}, func(File) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = Scan(ctx, Options{Root: root, MaxBytes: 100}, func(File) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	// Cancelled between device folders.
	ctx, cancel = context.WithCancel(context.Background())
	seen := 0
	_, err = Scan(ctx, Options{Root: root, MaxBytes: 100}, func(f File) error {
		if seen++; f.Name == "b-2.json" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || seen != 2 {
		t.Fatalf("cancel between devices: %v %d", err, seen)
	}
}

func TestScanRefusesUnqualifiedFilesystem(t *testing.T) {
	root := appFolder(t)
	swap(t, &fstatfs, func(int, *unix.Statfs_t) error { return unix.EIO })
	_, _, err := collect(t, Options{Root: root, MaxBytes: 100})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != "scout_root_unqualified" {
		t.Fatalf("%v", err)
	}
}

func TestEvalSymlinksFailure(t *testing.T) {
	swap(t, &evalSymlinks, func(string) (string, error) { return "", unix.EIO })
	if _, _, err := collect(t, Options{Root: "/x", MaxBytes: 1}); err == nil || err.Error() != "scout_root_invalid" {
		t.Fatalf("%v", err)
	}
	if found := appFolders("/x"); found != nil {
		t.Fatalf("%v", found)
	}
}

func TestCheckConfirmsTheLayoutWithoutReading(t *testing.T) {
	root := appFolder(t)
	got, err := Check(context.Background(), root)
	if err != nil || got != root {
		t.Fatalf("%q %v", got, err)
	}
	for _, dir := range []string{"relative", filepath.Join(root, "sessions")} {
		if _, err := Check(context.Background(), dir); err == nil || err.Error() != "scout_root_invalid" {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	swap(t, &fstatfs, func(int, *unix.Statfs_t) error { return unix.EIO })
	if _, err := Check(context.Background(), root); err == nil || err.Error() != "scout_root_unqualified" {
		t.Fatalf("unqualified: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Check(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
