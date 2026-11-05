//go:build darwin

package fileprivacy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ourostack/m365crawl/internal/errs"
	"golang.org/x/sys/unix"
)

func TestPrivateImageDarwinEmptyACLNativeContract(t *testing.T) {
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "empty-acl.png"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	api, err := privateDarwinLoad()
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	errno := api.errno()
	if errno == nil {
		t.Fatal("native errno address unavailable")
	}
	for _, fixture := range []string{"as-created", "explicitly-absent"} {
		if fixture == "explicitly-absent" {
			if output, err := exec.Command("/bin/chmod", "-N", file.Name()).CombinedOutput(); err != nil {
				t.Fatal("controlled native absent ACL fixture failed", err, string(output))
			}
		}
		*errno = 0
		acl := api.getFD(file.Fd(), 0x100)
		code := *errno
		if acl == 0 {
			t.Logf("%s native ACL: acl_present=false errno=%d", fixture, code)
			if code != int32(unix.ENOENT) {
				t.Error("native absent-ACL result differs from primary implementation", code)
			}
		} else {
			valid := api.valid(acl)
			*errno = 0
			var entry uintptr
			result := api.entry(acl, 0, &entry)
			code := *errno
			freed := api.free(acl)
			t.Logf("%s native ACL: acl_present=true valid=%d first_entry=%d entry_present=%v errno=%d free=%d", fixture, valid, result, entry != 0, code, freed)
			if valid != 0 || result != -1 || entry != 0 || code != int32(unix.EINVAL) || freed != 0 {
				t.Error("native empty-ACL contract differs from documented admission assumption")
			}
		}
		runtime.KeepAlive(file)
		if err := Temporary(file); err != nil {
			t.Error("native private ACL fixture failed admission", fixture, err)
		}
	}
}

func TestPrivateImageDarwinInheritedACLRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/chmod", "+a", "group:staff allow read,file_inherit,only_inherit", dir).CombinedOutput(); err != nil {
		t.Fatal("controlled native inherited ACL fixture failed", err, string(output))
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	var coded *errs.Coded
	if err := Parent(parent); !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
		t.Error("mode-safe parent with foreign file-inheritable ACL admitted", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "inherited.png"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("native fixture must retain0600 despite ACL inheritance", info, err)
	}
	coded = nil
	if err := Temporary(file); !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
		t.Error("mode0600 child with inherited foreign read ACL admitted", err)
	}
}

func TestPrivateImageDarwinIncompleteQueryIsNotAbsence(t *testing.T) {
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "incomplete-query.png"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	if output, err := exec.Command("/bin/chmod", "+a", "group:staff allow read", file.Name()).CombinedOutput(); err != nil {
		t.Fatal("controlled native foreign ACL fixture failed", err, string(output))
	}
	base, err := privateDarwinLoad()
	if err != nil {
		t.Fatal(err)
	}
	if err := Temporary(file); err == nil {
		t.Fatal("actual foreign ACL fixture incorrectly admitted before query fault")
	}
	api := base
	api.getFD = func(uintptr, int32) uintptr {
		*base.errno() = int32(unix.ENOENT)
		return 0
	}
	original := privateDarwinLoad
	t.Cleanup(func() { privateDarwinLoad = original })
	privateDarwinLoad = func() (darwinACL, error) { return api, nil }
	var coded *errs.Coded
	if err := Temporary(file); !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
		t.Fatal("incomplete security query masquerading as absence admitted foreign ACL", err)
	}
}

func TestPrivateImageDarwinACLQueryFailuresRefuse(t *testing.T) {
	base, err := privateDarwinLoad()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := purego.Dlclose(lib); err != nil {
			t.Error(err)
		}
	})
	address, err := purego.Dlsym(lib, "acl_init")
	if err != nil || address == 0 {
		t.Fatal("native empty ACL allocator unavailable", err)
	}
	var newACL func(int32) uintptr
	purego.RegisterFunc(&newACL, address)
	for _, boundary := range []string{"load", "nil-acl", "query-error", "invalid-acl", "nil-errno", "entry-error", "unexpected-entry", "unexpected-status", "free", "empty-acl-copy"} {
		t.Run(boundary, func(t *testing.T) {
			file, err := os.OpenFile(filepath.Join(t.TempDir(), "private.png"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := Temporary(file); err != nil {
				t.Fatal("actual empty-ACL private handle failed before query fault", err)
			}
			original := privateDarwinLoad
			t.Cleanup(func() { privateDarwinLoad = original })
			api := base
			api.getFD = func(uintptr, int32) uintptr {
				acl := newACL(0)
				if acl == 0 {
					t.Fatal("actual native empty ACL allocation failed")
				}
				return acl
			}
			sentinel := errors.New("synthetic native ACL query failure")
			var loadErr error
			switch boundary {
			case "load":
				loadErr = sentinel
			case "nil-acl":
				api.getFD = func(uintptr, int32) uintptr { return 0 }
			case "query-error":
				api.getFD = func(uintptr, int32) uintptr {
					*base.errno() = int32(unix.EIO)
					return 0
				}
			case "invalid-acl":
				api.valid = func(uintptr) int32 { return -1 }
			case "nil-errno":
				api.errno = func() *int32 { return nil }
			case "entry-error":
				api.entry = func(acl uintptr, index int32, entry *uintptr) int32 {
					result := base.entry(acl, index, entry)
					*base.errno() = int32(unix.EIO)
					return result
				}
			case "unexpected-entry":
				api.entry = func(_ uintptr, _ int32, entry *uintptr) int32 {
					*entry = 1
					*base.errno() = int32(unix.EINVAL)
					return -1
				}
			case "unexpected-status":
				api.entry = func(uintptr, int32, *uintptr) int32 { return 1 }
			case "free":
				api.free = func(acl uintptr) int32 {
					if result := base.free(acl); result != 0 {
						t.Error("actual native ACL fixture cleanup failed", result)
					}
					return -1
				}
			}
			privateDarwinLoad = func() (darwinACL, error) { return api, loadErr }
			err = Temporary(file)
			if boundary == "empty-acl-copy" {
				if err != nil {
					t.Fatal("valid native empty ACL copy failed admission", err)
				}
				return
			}
			var coded *errs.Coded
			if !errors.As(err, &coded) || coded.Code != "image_output_unprotected" ||
				boundary == "load" && !errors.Is(err, sentinel) {
				t.Fatal("unknown native ACL query/cleanup became private output", err)
			}
		})
	}
}

func TestPrivateImageDarwinACLLoaderFailuresRefuse(t *testing.T) {
	if _, err := privateDarwinLoad(); err != nil {
		t.Fatal("native library fixture unavailable", err)
	}
	for _, boundary := range []string{"open", "symbol", "zero-symbol", "close"} {
		t.Run(boundary, func(t *testing.T) {
			oldOpen, oldSymbol, oldClose := privateDarwinOpen, privateDarwinSymbol, privateDarwinClose
			t.Cleanup(func() {
				privateDarwinOpen, privateDarwinSymbol, privateDarwinClose = oldOpen, oldSymbol, oldClose
			})
			sentinel := errors.New("synthetic native ACL loader failure")
			switch boundary {
			case "open":
				privateDarwinOpen = func(string, int) (uintptr, error) { return 0, sentinel }
			default:
				privateDarwinSymbol = func(lib uintptr, name string) (uintptr, error) {
					if name == "acl_valid" {
						if boundary == "zero-symbol" {
							return 0, nil
						}
						return 0, sentinel
					}
					return oldSymbol(lib, name)
				}
				if boundary == "close" {
					privateDarwinClose = func(lib uintptr) error { return errors.Join(oldClose(lib), sentinel) }
				}
			}

			_, err := loadDarwinACL()
			if err == nil || boundary != "zero-symbol" && !errors.Is(err, sentinel) {
				t.Fatal("unavailable native ACL binding became successful admission", err)
			}
		})
	}
}
