//go:build !windows

package fileprivacy

import (
	"os"
	"syscall"
)

func platformPrivate(f *os.File, directory bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Geteuid()) {
		return privateError()
	}
	if directory {
		if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return privateError()
		}
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return privateError()
	}
	return nil
}
