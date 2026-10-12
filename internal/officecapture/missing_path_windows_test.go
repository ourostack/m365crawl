//go:build windows

package officecapture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

type windowsMissingStatusClaim []byte

func (windowsMissingStatusClaim) Error() string { return "wrapped native absence claim" }

func (windowsMissingStatusClaim) As(target any) bool {
	status, ok := target.(*windows.NTStatus)
	if ok {
		*status = windows.STATUS_OBJECT_NAME_NOT_FOUND
	}
	return ok
}

func TestWindowsOnlyDirectNativeAbsenceIsSkippable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cause   error
		missing bool
	}{
		{"direct-missing", windows.STATUS_OBJECT_NAME_NOT_FOUND, true},
		{"direct-permission", windows.STATUS_ACCESS_DENIED, false},
		{"wrapped-missing", fmt.Errorf("wrapped: %w", windows.STATUS_OBJECT_NAME_NOT_FOUND), false},
		{"joined-missing", errors.Join(windows.STATUS_OBJECT_NAME_NOT_FOUND, os.ErrPermission), false},
		{"noncomparable-as-claim", windowsMissingStatusClaim{1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			create := inputNTCreate
			t.Cleanup(func() { inputNTCreate = create })
			inputNTCreate = func(*windows.Handle, uint32, *windows.OBJECT_ATTRIBUTES,
				*windows.IO_STATUS_BLOCK, *int64, uint32, uint32, uint32, uint32, uintptr, uint32) error {
				return tc.cause
			}
			input, err := openInput(path, false)
			if input != nil || err == nil || errors.Is(err, os.ErrNotExist) != tc.missing {
				t.Fatal("wrapped or joined native status crossed the direct-only absence boundary")
			}
		})
	}
}

func TestWindowsMissingOrdinaryDirectoryIsRecognizableAbsence(t *testing.T) {
	path, _ := ordinaryFixture(t)
	input, err := openInput(filepath.Join(filepath.Dir(path), "missing"), true)
	if input != nil {
		if err := input.close(); err != nil {
			t.Fatal("unexpected missing-path handle did not close")
		}

		t.Fatal("missing ordinary directory unexpectedly opened")
	}
	var status windows.NTStatus
	if errors.As(err, &status) {
		t.Logf("native_status=0x%08x dos_errno=%d recognized_absence=%t",
			uint32(status), status.Errno(), errors.Is(status.Errno(), os.ErrNotExist))
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing ordinary directory was not exposed as recognizable absence")
	}
}

func TestWindowsMissingDirectoryCloseFailureIsNotSkippableAbsence(t *testing.T) {
	path, _ := ordinaryFixture(t)
	closeFile := jsonClose
	t.Cleanup(func() { jsonClose = closeFile })
	jsonClose = func(file *os.File) error {
		return errors.Join(closeFile(file), os.ErrPermission)
	}
	input, err := openInput(filepath.Join(filepath.Dir(path), "missing"), true)
	if input != nil || err == nil || errors.Is(err, os.ErrNotExist) || !errors.Is(err, os.ErrPermission) {
		t.Fatal("missing-path cleanup failure became skippable ordinary absence")
	}
}

func TestWindowsDataOpenAbsencePreservesMetadataCloseFailure(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "close-fails"}[closeFails], func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			create, closeFile := inputNTCreate, jsonClose
			t.Cleanup(func() { inputNTCreate, jsonClose = create, closeFile })
			calls := 0
			inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
				status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
				calls++
				if calls == 2 {
					return windows.STATUS_OBJECT_NAME_NOT_FOUND
				}
				return create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
			}
			closed := 0
			jsonClose = func(file *os.File) error {
				closed++
				err := closeFile(file)
				if closeFails && closed == 1 {
					return errors.Join(err, os.ErrPermission)
				}
				return err
			}
			input, err := openInput(path, false)
			if input != nil || err == nil || errors.Is(err, os.ErrNotExist) == closeFails ||
				(closeFails && !errors.Is(err, os.ErrPermission)) || closed < 2 {
				t.Fatal("data-open absence ignored metadata-close failure or failed to close held inputs")
			}
		})
	}
}
