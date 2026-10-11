//go:build windows

package officecapture

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsInputQualificationNativeDiagnostic(t *testing.T) {
	path, _ := ordinaryFixture(t)
	root, info, volume, create := inputWinRoot, inputWinInfo, inputWinVolume, inputNTCreate
	t.Cleanup(func() {
		inputWinRoot, inputWinInfo, inputWinVolume, inputNTCreate = root, info, volume, create
	})
	record := func(stage string, err error) {
		var status windows.NTStatus
		var errno syscall.Errno
		switch {
		case err == nil:
			t.Logf("operation=%s native_success=true", stage)
		case errors.As(err, &status):
			t.Logf("operation=%s ntstatus=0x%08x", stage, uint32(status))
		case errors.As(err, &errno):
			t.Logf("operation=%s win32=%d", stage, uint32(errno))
		default:
			t.Logf("operation=%s failure=non-native", stage)
		}
	}
	inputWinRoot = func(name *uint16, access, share uint32, security *windows.SecurityAttributes, disposition, flags uint32, template windows.Handle) (windows.Handle, error) {
		handle, err := root(name, access, share, security, disposition, flags, template)
		record("volume-root-open", err)
		return handle, err
	}
	inputWinInfo = func(handle windows.Handle, data *windows.ByHandleFileInformation) error {
		err := info(handle, data)
		record("file-information", err)
		if err == nil {
			t.Logf("attributes=0x%08x", data.FileAttributes)
		}
		return err
	}
	inputWinVolume = func(handle windows.Handle, name *uint16, nameSize uint32, serial, maximum, flags *uint32, fs *uint16, fsSize uint32) error {
		err := volume(handle, name, nameSize, serial, maximum, flags, fs, fsSize)
		record("volume-information", err)
		return err
	}
	inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
		status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
		err := create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
		stage := "component-metadata-open"
		if access&windows.FILE_READ_DATA != 0 {
			stage = "component-data-open"
		}
		record(stage, err)
		return err
	}
	input, err := openInput(path, false)
	if err != nil {
		record("qualification-terminal", err)
		t.Fatal("ordinary native input refused; preceding operation/code records identify the boundary")
	}
	if err := input.close(); err != nil {
		t.Fatal("native diagnostic handles did not close")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("owned source fixture disappeared")
	}
}
