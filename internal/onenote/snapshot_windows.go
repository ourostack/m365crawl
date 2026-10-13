//go:build windows

package onenote

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func snapshotSingleLink(file *os.File) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("unsafe index links")
	}
	return nil
}

func snapshotIdentity(info os.FileInfo) string {
	native, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", native.CreationTime.HighDateTime, native.CreationTime.LowDateTime)
}
