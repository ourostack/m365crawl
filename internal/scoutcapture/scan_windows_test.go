//go:build windows

package scoutcapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// opens records every name the scanner opens and the access it asks for.
type opens struct{ names, data []string }

func recordOpens(t *testing.T) *opens {
	t.Helper()
	got := &opens{}
	real := winNTCreate
	swap(t, &winNTCreate, func(h *windows.Handle, access uint32, oa *windows.OBJECT_ATTRIBUTES, iosb *windows.IO_STATUS_BLOCK, size *int64, attrs, share, disposition, options uint32, ea uintptr, eaLen uint32) error {
		name := oa.ObjectName.String()
		got.names = append(got.names, name)
		if access&windows.FILE_READ_DATA != 0 && options&windows.FILE_NON_DIRECTORY_FILE != 0 {
			got.data = append(got.data, name)
		}
		if options&windows.FILE_OPEN_NO_RECALL == 0 || options&windows.FILE_OPEN_REPARSE_POINT == 0 || disposition != windows.FILE_OPEN {
			t.Errorf("open of %s without no-recall, no-reparse open-existing options", name)
		}
		return real(h, access, oa, iosb, size, attrs, share, disposition, options, ea, eaLen)
	})
	return got
}

// listedAs rewrites the listing of one entry name, as OneDrive would mark it.
func listedAs(t *testing.T, name string, change func(*winEntry)) {
	t.Helper()
	real := winList
	swap(t, &winList, func(dir *os.File) ([]winEntry, error) {
		entries, err := real(dir)
		for i := range entries {
			if entries[i].Name == name {
				change(&entries[i])
			}
		}
		return entries, err
	})
}

func collect(t *testing.T, opts Options) (Result, map[string]File) {
	t.Helper()
	files := map[string]File{}
	res, err := Scan(context.Background(), opts, func(f File) error {
		files[f.Device+"/"+f.Name] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, files
}

func code(err error) string {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestWindowsScanReadsResidentFiles(t *testing.T) {
	root := appFolder(t)
	got := recordOpens(t)
	res, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if res.Root != root || res.Ignored != 3 || len(res.Devices) != 2 || !res.Devices[0].Listed || res.Devices[0].ID != devA {
		t.Fatalf("%+v", res)
	}
	a := files[devA+"/a-1.json"]
	if a.State != StateRead || string(a.Data) != `{"id":"a-1"}` || a.Size != int64(len(a.Data)) || a.Modified.IsZero() {
		t.Fatalf("%+v", a)
	}
	if len(files) != 3 || files[devB+"/c-3.json"].State != StateRead {
		t.Fatalf("%+v", files)
	}
	if !reflect.DeepEqual(got.data, []string{"a-1.json", "b-2.json", "c-3.json"}) {
		t.Fatalf("data opens %v", got.data)
	}
	// An unchanged file is not opened at all.
	got.names, got.data = nil, nil
	_, again := collect(t, Options{Root: root, MaxBytes: 1 << 20, Known: map[Key]Known{{devA, "a-1.json"}: {Size: a.Size, ModNanos: a.Modified.UnixNano()}}})
	if again[devA+"/a-1.json"].State != StateUnchanged {
		t.Fatalf("%+v", again[devA+"/a-1.json"])
	}
	for _, name := range got.names {
		if name == "a-1.json" {
			t.Fatal("unchanged file opened")
		}
	}
}

func TestWindowsCloudOnlyFilesAreNeverOpened(t *testing.T) {
	root := appFolder(t)
	got := recordOpens(t)
	listedAs(t, "a-1.json", func(e *winEntry) {
		e.Attributes, e.Tag = winAttrOffline|winAttrRecallOnDataAccess|winAttrReparsePoint|winAttrSparse, 0x9000401A
	})
	_, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if f := files[devA+"/a-1.json"]; f.State != StateCloudOnly || f.Reason != "scout_file_cloud_only" || f.Data != nil {
		t.Fatalf("%+v", f)
	}
	for _, name := range got.names {
		if name == "a-1.json" {
			t.Fatal("a cloud-only file was opened")
		}
	}
	if files[devA+"/b-2.json"].State != StateRead {
		t.Fatalf("%+v", files)
	}
}

func TestWindowsListingRefusals(t *testing.T) {
	root := appFolder(t)
	listedAs(t, "a-1.json", func(e *winEntry) { e.Attributes, e.Tag = winAttrReparsePoint, 0xA000000C })
	listedAs(t, "b-2.json", func(e *winEntry) { e.Size = 1 << 30 })
	listedAs(t, devB, func(e *winEntry) { e.Attributes |= winAttrOffline })
	res, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if files[devA+"/a-1.json"].State != StateUnsafe || files[devA+"/b-2.json"].State != StateTooLarge {
		t.Fatalf("%+v", files)
	}
	if res.Devices[1].Listed || res.Devices[1].Reason != "scout_device_unreadable" {
		t.Fatalf("%+v", res.Devices)
	}
}

// The attributes on the open file decide again: a file that became cloud-only after it was
// listed is reported cloud-only and never opened for data.
func TestWindowsOpenHandleRecheck(t *testing.T) {
	root := appFolder(t)
	got := recordOpens(t)
	real := winAttrTag
	swap(t, &winAttrTag, func(f *os.File) (uint32, uint32, error) {
		attrs, tag, err := real(f)
		if f.Name() == "a-1.json" {
			attrs |= winAttrRecallOnDataAccess
		}
		return attrs, tag, err
	})
	_, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if files[devA+"/a-1.json"].State != StateCloudOnly {
		t.Fatalf("%+v", files[devA+"/a-1.json"])
	}
	for _, name := range got.data {
		if name == "a-1.json" {
			t.Fatal("opened for data")
		}
	}
}

func TestWindowsChangeDuringRead(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"listing size differs": func(t *testing.T) { listedAs(t, "a-1.json", func(e *winEntry) { e.Size++ }) },
		"attributes change after the read": func(t *testing.T) {
			real, calls := winAttrTag, 0
			swap(t, &winAttrTag, func(f *os.File) (uint32, uint32, error) {
				attrs, tag, err := real(f)
				if f.Name() == "a-1.json" {
					if calls++; calls == 3 {
						attrs |= winAttrSparse
					}
				}
				return attrs, tag, err
			})
		},
		"attributes change between opens": func(t *testing.T) {
			real, calls := winAttrTag, 0
			swap(t, &winAttrTag, func(f *os.File) (uint32, uint32, error) {
				attrs, tag, err := real(f)
				if f.Name() == "a-1.json" {
					if calls++; calls == 2 {
						attrs |= winAttrPinned
					}
				}
				return attrs, tag, err
			})
		},
		"short read": func(t *testing.T) {
			swap(t, &winReadAll, func(r io.Reader) ([]byte, error) { return []byte("{"), nil })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := appFolder(t)
			setup(t)
			_, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
			if f := files[devA+"/a-1.json"]; f.State != StateFailed || f.Reason != "scout_file_changed_during_read" {
				t.Fatalf("%+v", f)
			}
		})
	}
}

func TestWindowsReadFailures(t *testing.T) {
	cases := map[string]struct {
		setup  func(t *testing.T)
		state  string
		reason string
	}{
		"cloud refusal on data open": {func(t *testing.T) {
			real := winNTCreate
			swap(t, &winNTCreate, func(h *windows.Handle, access uint32, oa *windows.OBJECT_ATTRIBUTES, iosb *windows.IO_STATUS_BLOCK, size *int64, attrs, share, disposition, options uint32, ea uintptr, eaLen uint32) error {
				if oa.ObjectName.String() == "a-1.json" && access&windows.FILE_READ_DATA != 0 {
					return windows.STATUS_CLOUD_FILE_ACCESS_DENIED
				}
				return real(h, access, oa, iosb, size, attrs, share, disposition, options, ea, eaLen)
			})
		}, StateCloudOnly, "scout_file_cloud_only"},
		"other open failure": {func(t *testing.T) {
			real := winNTCreate
			swap(t, &winNTCreate, func(h *windows.Handle, access uint32, oa *windows.OBJECT_ATTRIBUTES, iosb *windows.IO_STATUS_BLOCK, size *int64, attrs, share, disposition, options uint32, ea uintptr, eaLen uint32) error {
				if oa.ObjectName.String() == "a-1.json" {
					return windows.STATUS_ACCESS_DENIED
				}
				return real(h, access, oa, iosb, size, attrs, share, disposition, options, ea, eaLen)
			})
		}, StateFailed, "scout_file_unreadable"},
		"cloud refusal on read": {func(t *testing.T) {
			swap(t, &winReadAll, func(io.Reader) ([]byte, error) { return nil, syscall.Errno(395) })
		}, StateCloudOnly, "scout_file_cloud_only"},
		"other read failure": {func(t *testing.T) {
			swap(t, &winReadAll, func(io.Reader) ([]byte, error) { return nil, syscall.Errno(5) })
		}, StateFailed, "scout_file_unreadable"},
		"read past the limit": {func(t *testing.T) {
			swap(t, &winReadAll, func(io.Reader) ([]byte, error) { return make([]byte, 101), nil })
		}, StateTooLarge, "scout_file_too_large"},
		"identity unavailable": {func(t *testing.T) {
			swap(t, &winInfo, func(windows.Handle, *windows.ByHandleFileInformation) error { return windows.ERROR_ACCESS_DENIED })
		}, StateFailed, "scout_file_unreadable"},
		"identity lost after the read": {func(t *testing.T) {
			real, calls := winInfo, 0
			swap(t, &winInfo, func(h windows.Handle, info *windows.ByHandleFileInformation) error {
				if calls++; calls == 3 {
					return windows.ERROR_ACCESS_DENIED
				}
				return real(h, info)
			})
		}, StateFailed, "scout_file_unreadable"},
		"attributes unavailable on the data handle": {func(t *testing.T) {
			real, calls := winAttrTag, 0
			swap(t, &winAttrTag, func(f *os.File) (uint32, uint32, error) {
				if f.Name() == "a-1.json" {
					if calls++; calls == 2 {
						return 0, 0, windows.ERROR_ACCESS_DENIED
					}
				}
				return real(f)
			})
		}, StateFailed, "scout_file_unreadable"},
		"cloud refusal on data open via errno": {func(t *testing.T) {
			real := winNTCreate
			swap(t, &winNTCreate, func(h *windows.Handle, access uint32, oa *windows.OBJECT_ATTRIBUTES, iosb *windows.IO_STATUS_BLOCK, size *int64, attrs, share, disposition, options uint32, ea uintptr, eaLen uint32) error {
				if oa.ObjectName.String() == "a-1.json" {
					return syscall.Errno(377)
				}
				return real(h, access, oa, iosb, size, attrs, share, disposition, options, ea, eaLen)
			})
		}, StateCloudOnly, "scout_file_cloud_only"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := appFolder(t)
			c.setup(t)
			seen := false
			_, err := Scan(context.Background(), Options{Root: root, MaxBytes: 100}, func(f File) error {
				if f.Name == "a-1.json" {
					seen = true
					if f.State != c.state || f.Reason != c.reason || f.Data != nil {
						t.Errorf("%+v", f)
					}
				}
				return nil
			})
			if err != nil || !seen {
				t.Fatal(err, seen)
			}
		})
	}
}

func TestWindowsRootRefusals(t *testing.T) {
	root := appFolder(t)
	file := filepath.Join(root, "sessions", "sessions-index.json")
	for _, bad := range []string{"relative", root + `\..\x`, `\\server\share\Apps\Scout`, root + `:stream`, filepath.Join(root, "missing"), file} {
		if _, err := Check(context.Background(), bad); code(err) != "scout_root_invalid" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if got, err := Check(context.Background(), root); err != nil || got != root {
		t.Fatalf("%q %v", got, err)
	}
	t.Run("not a fixed drive", func(t *testing.T) {
		swap(t, &winDriveType, func(*uint16) uint32 { return windows.DRIVE_REMOTE })
		if _, err := Check(context.Background(), root); code(err) != "scout_root_unqualified" {
			t.Fatal(err)
		}
	})
	t.Run("not NTFS", func(t *testing.T) {
		swap(t, &winVolume, func(windows.Handle, *uint16, uint32, *uint32, *uint32, *uint32, *uint16, uint32) error {
			return windows.ERROR_ACCESS_DENIED
		})
		if _, err := Scan(context.Background(), Options{Root: root}, func(File) error { return nil }); code(err) != "scout_root_unqualified" {
			t.Fatal(err)
		}
	})
	t.Run("drive root unavailable", func(t *testing.T) {
		swap(t, &winOpenRoot, func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return windows.InvalidHandle, windows.ERROR_ACCESS_DENIED
		})
		if _, err := Check(context.Background(), root); code(err) != "scout_root_invalid" {
			t.Fatal(err)
		}
	})
	t.Run("drive root name", func(t *testing.T) {
		if _, err := Check(context.Background(), "C:\\a\x00b"); code(err) != "scout_root_invalid" {
			t.Fatal(err)
		}
	})
}

func TestWindowsLinksAreNotFollowed(t *testing.T) {
	root := appFolder(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, devB, "z.json"), `{}`)
	link := filepath.Join(filepath.Dir(root), "linked")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, root).CombinedOutput(); err != nil {
		t.Skipf("mklink: %v %s", err, out)
	}
	if _, err := Check(context.Background(), link); code(err) != "scout_root_invalid" {
		t.Fatalf("a root through a junction: %v", err)
	}
	devices := filepath.Join(root, "sessions", "devices")
	if err := os.RemoveAll(filepath.Join(devices, devB)); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(devices, devB), filepath.Join(outside, devB)).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	res, files := collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if res.Devices[1].Listed || res.Devices[1].Reason != "scout_device_unreadable" || len(files) != 2 {
		t.Fatalf("%+v %+v", res, files)
	}
	if err := os.Symlink(filepath.Join(outside, devB, "z.json"), filepath.Join(devices, devA, "s-1.json")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	_, files = collect(t, Options{Root: root, MaxBytes: 1 << 20})
	if f := files[devA+"/s-1.json"]; f.State != StateUnsafe {
		t.Fatalf("%+v", f)
	}
}

func TestWindowsListingFailures(t *testing.T) {
	root := appFolder(t)
	t.Run("devices folder", func(t *testing.T) {
		swap(t, &winList, func(*os.File) ([]winEntry, error) { return nil, windows.ERROR_ACCESS_DENIED })
		if _, err := Scan(context.Background(), Options{Root: root}, func(File) error { return nil }); code(err) != "scout_devices_unreadable" {
			t.Fatal(err)
		}
	})
	t.Run("device folder", func(t *testing.T) {
		real, calls := winList, 0
		swap(t, &winList, func(f *os.File) ([]winEntry, error) {
			if calls++; calls > 1 {
				return nil, windows.ERROR_ACCESS_DENIED
			}
			return real(f)
		})
		res, files := collect(t, Options{Root: root})
		if len(files) != 0 || res.Devices[0].Listed || res.Devices[0].Reason != "scout_device_unreadable" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("device folder open", func(t *testing.T) {
		real := winAttrTag
		swap(t, &winAttrTag, func(f *os.File) (uint32, uint32, error) {
			if f.Name() == devA {
				return 0, 0, windows.ERROR_ACCESS_DENIED
			}
			return real(f)
		})
		res, _ := collect(t, Options{Root: root})
		if res.Devices[0].Listed || !res.Devices[1].Listed {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("malformed listing", func(t *testing.T) {
		swap(t, &winInfoEx, func(windows.Handle, uint32, *byte, uint32) error { return nil })
		dir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		if _, err := listDir(dir); err == nil {
			t.Fatal("a zero listing was accepted")
		}
	})
	t.Run("listing error", func(t *testing.T) {
		swap(t, &winInfoEx, func(windows.Handle, uint32, *byte, uint32) error { return windows.ERROR_ACCESS_DENIED })
		dir, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		if _, err := listDir(dir); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestWindowsEmptyDeviceFolderAndLargeListing(t *testing.T) {
	root := appFolder(t)
	empty := filepath.Join(root, "sessions", "devices", "00000000000000000000000000000000")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	// Enough entries to need more than one listing call.
	for i := 0; i < 900; i++ {
		write(t, filepath.Join(root, "sessions", "devices", devB, fmt.Sprintf("%s-%04d.json", strings.Repeat("n", 120), i)), `{}`)
	}
	res, files := collect(t, Options{Root: root, Known: map[Key]Known{}, MaxBytes: 1 << 20})
	if !res.Devices[0].Listed || len(files) != 903 {
		t.Fatalf("%d %+v", len(files), res.Devices)
	}
}

func TestWindowsScanStops(t *testing.T) {
	root := appFolder(t)
	stop := errors.New("stop")
	if _, err := Scan(context.Background(), Options{Root: root, MaxBytes: 1 << 20}, func(File) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := Scan(ctx, Options{Root: root, MaxBytes: 1 << 20}, func(File) error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Scan(ctx, Options{Root: root}, func(File) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), Options{Root: root, MaxBytes: 1 << 20}, func(File) error { panic("boom") }); code(err) != "scout_capture_failed" {
		t.Fatal(err)
	}
}

func TestWindowsDiscover(t *testing.T) {
	base := t.TempDir()
	business := filepath.Join(base, "OneDrive - Contoso")
	scout := filepath.Join(business, "Apps", "Scout")
	if err := os.MkdirAll(filepath.Join(scout, "sessions", "devices"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(business, "Apps", "Other", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(business, "Apps", "Bare"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(business, "Apps", "notes.txt"), "x")
	personal := filepath.Join(base, "OneDrive")
	if err := os.MkdirAll(personal, 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"OneDriveCommercial": business, "OneDrive": strings.ToUpper(business), "OneDriveConsumer": personal}
	swap(t, &winGetenv, func(k string) string { return env[k] })
	found, err := Discover(context.Background(), "ignored")
	if err != nil || !reflect.DeepEqual(found, []string{scout}) {
		t.Fatalf("%v %v", found, err)
	}
	t.Run("apps listing fails", func(t *testing.T) {
		swap(t, &winList, func(*os.File) ([]winEntry, error) { return nil, windows.ERROR_ACCESS_DENIED })
		if found, err := Discover(context.Background(), ""); err != nil || len(found) != 0 {
			t.Fatalf("%v %v", found, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Discover(ctx, ""); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("cancelled between roots", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		swap(t, &winGetenv, func(k string) string { cancel(); return env[k] })
		if _, err := Discover(ctx, ""); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestWindowsOpenChildRefusesPathNames(t *testing.T) {
	for _, name := range []string{"", `a\b`, "a/b", "a:b", "a\x00b"} {
		if f, err := openChild(windows.InvalidHandle, name, false, windows.FILE_READ_ATTRIBUTES); err == nil {
			_ = f.Close()
			t.Errorf("%q accepted", name)
		}
	}
	if cloudErr(errors.New("x")) || cloudErr(windows.STATUS_ACCESS_DENIED) || !cloudErr(windows.STATUS_CLOUD_FILE_ACCESS_DENIED) {
		t.Fatal("cloud error classification")
	}
}
