//go:build darwin || linux

package onenote

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func snapshotSingleLink(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || native.Nlink != 1 {
		return errors.New("unsafe index links")
	}
	return nil
}

func snapshotIdentity(info os.FileInfo) string {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", native.Dev, native.Ino)
}
