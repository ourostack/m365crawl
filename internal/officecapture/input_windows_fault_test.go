//go:build windows

package officecapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsInputNativeFaultsRefuseBeforeCallback(t *testing.T) {
	for _, fault := range []string{"drive", "root", "root-info", "metadata-info", "bound-info",
		"volume", "filesystem", "provider", "root-type", "metadata-open", "data-open", "metadata-close", "bound-identity"} {
		t.Run(fault, func(t *testing.T) {
			path, _ := ordinaryFixture(t)
			drive, root, info, volume, create, closeFile := inputDriveType, inputWinRoot, inputWinInfo, inputWinVolume, inputNTCreate, jsonClose
			t.Cleanup(func() {
				inputDriveType, inputWinRoot, inputWinInfo, inputWinVolume, inputNTCreate, jsonClose = drive, root, info, volume, create, closeFile
			})
			if fault == "drive" {
				inputDriveType = func(*uint16) uint32 { return windows.DRIVE_REMOTE }
			}
			inputWinRoot = func(name *uint16, access, share uint32, security *windows.SecurityAttributes,
				disposition, options uint32, template windows.Handle) (windows.Handle, error) {
				if fault == "root" {
					return windows.InvalidHandle, windows.ERROR_ACCESS_DENIED
				}
				return root(name, access, share, security, disposition, options, template)
			}
			infoCalls := 0
			inputWinInfo = func(handle windows.Handle, result *windows.ByHandleFileInformation) error {
				infoCalls++
				if (fault == "root-info" && infoCalls == 1) || (fault == "metadata-info" && infoCalls == 2) ||
					(fault == "bound-info" && infoCalls == 3) {
					return windows.ERROR_ACCESS_DENIED
				}
				if err := info(handle, result); err != nil {
					return err
				}
				switch {
				case fault == "provider":
					result.FileAttributes |= windows.FILE_ATTRIBUTE_OFFLINE
				case fault == "root-type" && infoCalls == 1:
					result.FileAttributes &^= windows.FILE_ATTRIBUTE_DIRECTORY
				case fault == "bound-identity" && infoCalls == 3:
					result.FileIndexLow++
				}
				return nil
			}
			inputWinVolume = func(handle windows.Handle, name *uint16, nameSize uint32, serial, maxComponent, flags *uint32,
				filesystem *uint16, filesystemSize uint32) error {
				if fault == "volume" {
					return windows.ERROR_ACCESS_DENIED
				}
				if fault == "filesystem" {
					*filesystem = 'R'
					return nil
				}
				return volume(handle, name, nameSize, serial, maxComponent, flags, filesystem, filesystemSize)
			}
			creates := 0
			inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
				status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
				creates++
				if (fault == "metadata-open" && creates == 1) || (fault == "data-open" && creates == 2) {
					return windows.STATUS_ACCESS_DENIED
				}
				return create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
			}
			closed := 0
			jsonClose = func(file *os.File) error {
				closed++
				err := closeFile(file)
				if fault == "metadata-close" && closed == 1 {
					return errors.Join(err, os.ErrPermission)
				}
				return err
			}
			called := false
			err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
				func(*JSONCapture) error { called = true; return nil })
			var coded *ReadError
			if !errors.As(err, &coded) || coded.Code != "office_json_input_unqualified" || called {
				t.Fatal("unqualified native metadata/data/identity boundary admitted callback")
			}
		})
	}
}

func TestWindowsInputRejectsNoncanonicalNamesAndRootFile(t *testing.T) {
	path, _ := ordinaryFixture(t)
	for _, input := range []string{"relative.json", `\\server\share\recent.json`,
		path + ":stream", path + `\..\recent.json`, filepath.VolumeName(path) + `\`, filepath.VolumeName(path) + "\\\x00"} {
		if opened, err := openInput(input, false); err == nil || opened != nil {
			t.Fatal("noncanonical/non-file path became native input")
		}
	}
	opened, err := openInput(filepath.VolumeName(path)+`\`, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.close(); err != nil {
		t.Fatal(err)
	}
}
