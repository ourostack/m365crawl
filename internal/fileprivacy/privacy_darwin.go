//go:build darwin

package fileprivacy

import (
	"errors"
	"os"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

type darwinACL struct {
	getFD func(uintptr, int32) uintptr
	valid func(uintptr) int32
	entry func(uintptr, int32, *uintptr) int32
	free  func(uintptr) int32
	errno func() *int32
}

var (
	privateDarwinLoad   = sync.OnceValues(loadDarwinACL)
	privateDarwinOpen   = purego.Dlopen
	privateDarwinSymbol = purego.Dlsym
	privateDarwinClose  = purego.Dlclose
)

func loadDarwinACL() (darwinACL, error) {
	lib, err := privateDarwinOpen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return darwinACL{}, err
	}
	var api darwinACL
	for _, binding := range []struct {
		name   string
		target any
	}{
		{"acl_get_fd_np", &api.getFD},
		{"acl_valid", &api.valid},
		{"acl_get_entry", &api.entry},
		{"acl_free", &api.free},
		{"__error", &api.errno},
	} {
		address, err := privateDarwinSymbol(lib, binding.name)
		if err != nil || address == 0 {
			return darwinACL{}, errors.Join(privateError(), err, privateDarwinClose(lib))
		}
		purego.RegisterFunc(binding.target, address)
	}
	// The bindings retain LibSystem for the lifetime of the process.
	return api, nil
}

func privateACL(file *os.File) (err error) {
	api, err := privateDarwinLoad()
	if err != nil {
		return errors.Join(privateError(), err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	errno := api.errno()
	if errno == nil {
		return privateError()
	}
	*errno = 0
	acl := api.getFD(file.Fd(), 0x100) // ACL_TYPE_EXTENDED, queried on the already bound handle.
	if acl == 0 {
		// Darwin reports an absent ACL property as NULL/ENOENT, not an empty ACL copy.
		if *errno == int32(unix.ENOENT) {
			return nil
		}
		return privateError()
	}
	defer func() {
		if api.free(acl) != 0 {
			err = errors.Join(err, privateError())
		}
		runtime.KeepAlive(file)
	}()
	if api.valid(acl) != 0 {
		return privateError()
	}
	*errno = 0
	var entry uintptr
	// A valid, privately owned ACL copy has no first entry only when the index
	// is out of range (EINVAL). Any entry or other result refuses, including deny-only ACLs.
	if api.entry(acl, 0, &entry) != -1 || entry != 0 || *errno != int32(unix.EINVAL) {
		return privateError()
	}
	return nil
}
