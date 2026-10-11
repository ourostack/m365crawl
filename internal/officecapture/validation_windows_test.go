//go:build windows

package officecapture

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsHeldAndReopenedInputValidationFaults(t *testing.T) {
	for _, fault := range []string{"held-identity", "reopen", "path-identity", "reopen-close"} {
		t.Run(fault, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			input, err := openInput(path, false)
			if err != nil {
				t.Fatal(err)
			}
			info, root, closeFile := inputWinInfo, inputWinRoot, jsonClose
			t.Cleanup(func() {
				inputWinInfo, inputWinRoot, jsonClose = info, root, closeFile
				if err := input.close(); err != nil {
					t.Error(err)
				}
			})
			infos := 0
			inputWinInfo = func(handle windows.Handle, result *windows.ByHandleFileInformation) error {
				infos++
				if err := info(handle, result); err != nil {
					return err
				}
				if fault == "held-identity" || (fault == "path-identity" && infos > len(input.files)) {
					result.VolumeSerialNumber++
				}
				return nil
			}
			if fault == "reopen" {
				inputWinRoot = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
					return windows.InvalidHandle, windows.ERROR_ACCESS_DENIED
				}
			}
			if fault == "reopen-close" {
				jsonClose = func(file *os.File) error { return errors.Join(closeFile(file), os.ErrPermission) }
			}
			if err := input.validate(); err == nil {
				t.Fatal("changed/unverifiable/reopened-close input became a stable reusable source")
			}
		})
	}
}

func TestWindowsPrivateCaptureReaderOpenAndCloseFailuresRefuseClaims(t *testing.T) {
	for _, fault := range []string{"open", "close"} {
		t.Run(fault, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			create, closeFile := inputNTCreate, jsonClose
			t.Cleanup(func() { inputNTCreate, jsonClose = create, closeFile })
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
				func(capture *JSONCapture) error {
					if fault == "open" {
						inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
							status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
							if attributes.ObjectName.String() == "source.json" {
								return windows.STATUS_ACCESS_DENIED
							}
							return create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
						}
					} else {
						closes := 0
						jsonClose = func(file *os.File) error {
							err := closeFile(file)
							if file.Name() == "source.json" {
								closes++
								if closes == 2 {
									return errors.Join(err, os.ErrPermission)
								}
							}
							return err
						}
					}
					result, err := capture.ReadJSON(context.Background())
					if err == nil || len(result.Documents) != 0 {
						t.Fatal("unqualified private reader open/close returned decoded claims")
					}
					return err
				})
			if err == nil {
				t.Fatal("private reader failure became successful capture")
			}
		})
	}
}
