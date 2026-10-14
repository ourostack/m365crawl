//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var (
	policyOnce         sync.Once
	policyErr          error
	policyGet          func(int32, int32) int32
	policySet          func(int32, int32, int32) int32
	policyOpen         = purego.Dlopen
	policySymbol       = purego.Dlsym
	policyCloseLibrary = purego.Dlclose
	inputOpen          = unix.Openat
	inputStat          = unix.Fstat
	inputFS            = unix.Fstatfs
)

const permittedInputFlags = unix.UF_NODUMP | unix.UF_IMMUTABLE | unix.UF_APPEND | unix.UF_OPAQUE | unix.UF_HIDDEN | unix.UF_COMPRESSED | unix.UF_TRACKED |
	unix.SF_ARCHIVED | unix.SF_IMMUTABLE | unix.SF_APPEND | unix.SF_RESTRICTED | unix.SF_NOUNLINK

func loadPolicy() {
	lib, err := policyOpen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		policyErr = err
		return
	}
	get, getErr := policySymbol(lib, "getiopolicy_np")
	set, setErr := policySymbol(lib, "setiopolicy_np")
	if getErr != nil || setErr != nil {
		policyErr = errors.Join(getErr, setErr, policyCloseLibrary(lib))
		return
	}
	purego.RegisterFunc(&policyGet, get)
	purego.RegisterFunc(&policySet, set)
}

func withInputGuard(ctx context.Context, operation func() error) error {
	policyOnce.Do(loadPolicy)
	if policyErr != nil {
		return jsonError(ctx, "office_json_input_unqualified")
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		unlock := true
		var result error
		previous := int32(-1)
		defer func() {
			if recover() != nil {
				result = jsonError(ctx, "office_json_capture_failed")
			}
			if previous >= 0 && (policySet(3, 1, previous) != 0 || policyGet(3, 1) != previous) {
				unlock = false
				result = errors.Join(result, jsonError(ctx, "office_json_policy_restore_failed"))
			}
			if unlock {
				runtime.UnlockOSThread()
			}
			done <- result
		}()
		previous = policyGet(3, 1)
		if previous < 0 || policySet(3, 1, 1) != 0 || policyGet(3, 1) != 1 {
			result = jsonError(ctx, "office_json_input_unqualified")
			return
		}
		if ctx.Err() != nil {
			result = jsonError(ctx, "office_json_capture_failed")
			return
		}
		result = operation()
	}()
	return <-done
}

func inputSignature(file *os.File, directory bool) (string, error) {
	var info unix.Stat_t
	var fs unix.Statfs_t
	if err := inputStat(int(file.Fd()), &info); err != nil {
		return "", err
	}
	if err := inputFS(int(file.Fd()), &fs); err != nil {
		return "", err
	}
	if unix.ByteSliceToString(fs.Fstypename[:]) != "apfs" || fs.Flags&unix.MNT_LOCAL == 0 ||
		info.Flags & ^uint32(permittedInputFlags) != 0 {
		return "", errors.New("unqualified local input")
	}
	if directory {
		if info.Mode&unix.S_IFMT != unix.S_IFDIR {
			return "", errors.New("input is not a directory")
		}
		return fmt.Sprintf("%d:%d:%d", info.Dev, info.Ino, info.Flags), nil
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", errors.New("input is not regular")
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d", info.Dev, info.Ino, info.Size,
		info.Mtim.Sec, info.Mtim.Nsec, info.Flags), nil
}

func openInput(path string, directory bool) (_ *guardedInput, result error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("unqualified input path")
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == "CloudStorage" || component == "Mobile Documents" || component == ".icloud" {
			return nil, errors.New("provider input")
		}
	}
	input := &guardedInput{path: path, directory: directory}
	defer func() {
		if result != nil {
			result = errors.Join(result, input.close())
		}
	}()
	fd, err := inputOpen(unix.AT_FDCWD, "/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "/")
	input.files = append(input.files, root)
	signature, err := inputSignature(root, true)
	if err != nil {
		return nil, err
	}
	input.stamps = append(input.stamps, signature)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if path == "/" {
		if !directory {
			return nil, errors.New("directory is not a source file")
		}
		return input, nil
	}
	for i, part := range parts {
		isDirectory := directory || i < len(parts)-1
		flags := unix.O_RDONLY | unix.O_NOFOLLOW_ANY | unix.O_CLOEXEC
		if isDirectory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := inputOpen(int(input.file().Fd()), part, flags, 0)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), part)
		input.files = append(input.files, file)
		signature, err := inputSignature(file, isDirectory)
		if err != nil {
			return nil, err
		}
		input.stamps = append(input.stamps, signature)
	}
	return input, nil
}
