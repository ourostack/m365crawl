//go:build darwin

package scoutcapture

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// The I/O policy that decides whether touching a dataless (cloud-only) file downloads it:
// IOPOL_TYPE_VFS_MATERIALIZE_DATALESS_FILES for the calling thread, set to OFF.
const (
	policyType      = 3
	policyThread    = 1
	materializeOff  = 1
	cloudStorageDir = "Library/CloudStorage"
)

var (
	policyOnce   sync.Once
	policyErr    error
	policyGet    func(int32, int32) int32
	policySet    func(int32, int32, int32) int32
	policyOpen   = purego.Dlopen
	policySymbol = purego.Dlsym
	policyClose  = purego.Dlclose
	fstatfs      = unix.Fstatfs
)

func loadPolicy() {
	lib, err := policyOpen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		policyErr = err
		return
	}
	get, getErr := policySymbol(lib, "getiopolicy_np")
	set, setErr := policySymbol(lib, "setiopolicy_np")
	if getErr != nil || setErr != nil {
		policyErr = errors.Join(getErr, setErr, policyClose(lib))
		return
	}
	purego.RegisterFunc(&policyGet, get)
	purego.RegisterFunc(&policySet, set)
}

// withGuard runs operation on one locked thread whose dataless-file materialization is OFF, and
// restores the thread's previous policy afterwards. A thread whose policy cannot be restored is
// never returned to the runtime.
func withGuard(ctx context.Context, operation func() error) error {
	policyOnce.Do(loadPolicy)
	if policyErr != nil {
		return &Error{Code: "scout_guard_unavailable"}
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		unlock := true
		var result error
		previous := int32(-1)
		defer func() {
			if recover() != nil {
				result = &Error{Code: "scout_capture_failed"}
			}
			if previous >= 0 && (policySet(policyType, policyThread, previous) != 0 || policyGet(policyType, policyThread) != previous) {
				unlock = false
				result = errors.Join(result, &Error{Code: "scout_guard_restore_failed"})
			}
			if unlock {
				runtime.UnlockOSThread()
			}
			done <- result
		}()
		previous = policyGet(policyType, policyThread)
		if previous < 0 || policySet(policyType, policyThread, materializeOff) != 0 || policyGet(policyType, policyThread) != materializeOff {
			result = &Error{Code: "scout_guard_unavailable"}
			return
		}
		if err := ctx.Err(); err != nil {
			result = err
			return
		}
		result = operation()
	}()
	return <-done
}

// qualifiedFS admits only a local APFS volume, where OneDrive's placeholders are dataless files.
func qualifiedFS(fd int) bool {
	var fs unix.Statfs_t
	if fstatfs(fd, &fs) != nil {
		return false
	}
	return unix.ByteSliceToString(fs.Fstypename[:]) == "apfs" && fs.Flags&unix.MNT_LOCAL != 0
}

func dataless(st *unix.Stat_t) bool { return st.Flags&unix.SF_DATALESS != 0 }

// blocked reports the error a read of a dataless file returns while materialization is off.
func blocked(err error) bool { return errors.Is(err, unix.EDEADLK) }

func listNames(dir string) ([]string, error) {
	f, err := openDir(unix.AT_FDCWD, dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readNames(f)
}

// oneDriveRoots are the OneDrive folders the OneDrive app keeps under ~/Library/CloudStorage,
// one per signed-in account, named OneDrive-<account name>. Shared libraries are skipped: they
// hold no app folders.
func oneDriveRoots(home string) []string {
	dir := filepath.Join(home, cloudStorageDir)
	names, err := listNames(dir)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, name := range names {
		if strings.HasPrefix(name, "OneDrive-") && !strings.HasPrefix(name, "OneDrive-SharedLibraries") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}
