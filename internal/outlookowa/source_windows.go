//go:build windows

package outlookowa

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func unsafeEntry(info os.FileInfo) bool {
	attr, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&os.ModeSymlink != 0 || !ok || attr.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func sourceIdentity(path string, _ os.FileInfo) (string, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", sourceFailure("source_unreadable", err)
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", sourceFailure("source_unreadable", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", sourceFailure("source_unreadable", err)
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", sourceFailure("unsafe_source", nil)
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
