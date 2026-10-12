//go:build windows

package officecapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	inputNTCreate  = windows.NtCreateFile
	inputWinInfo   = windows.GetFileInformationByHandle
	inputWinVolume = windows.GetVolumeInformationByHandle
	inputDriveType = windows.GetDriveType
	inputWinRoot   = windows.CreateFile
)

func withInputGuard(ctx context.Context, operation func() error) (result error) {
	defer func() {
		if recover() != nil {
			result = jsonError(ctx, "office_json_capture_failed")
		}
	}()
	if ctx.Err() != nil {
		return jsonError(ctx, "office_json_capture_failed")
	}
	return operation()
}

func inputSignature(file *os.File, directory bool) (string, error) {
	var info windows.ByHandleFileInformation
	if err := inputWinInfo(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	const permitted = windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_ATTRIBUTE_READONLY |
		windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM | windows.FILE_ATTRIBUTE_DIRECTORY |
		windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_TEMPORARY | windows.FILE_ATTRIBUTE_COMPRESSED |
		windows.FILE_ATTRIBUTE_NOT_CONTENT_INDEXED | windows.FILE_ATTRIBUTE_ENCRYPTED |
		windows.FILE_ATTRIBUTE_INTEGRITY_STREAM | windows.FILE_ATTRIBUTE_NO_SCRUB_DATA
	if info.FileAttributes & ^uint32(permitted) != 0 {
		return "", errors.New("provider or unknown input attributes")
	}
	var fs [32]uint16
	if err := inputWinVolume(windows.Handle(file.Fd()), nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))); err != nil {
		return "", err
	}
	if windows.UTF16ToString(fs[:]) != "NTFS" ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return "", errors.New("unqualified filesystem or input type")
	}
	identity := fmt.Sprintf("%d:%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh,
		info.FileIndexLow, info.FileAttributes)
	if directory {
		return identity, nil
	}
	return fmt.Sprintf("%s:%d:%d:%d:%d", identity, info.FileSizeHigh, info.FileSizeLow,
		info.LastWriteTime.HighDateTime, info.LastWriteTime.LowDateTime), nil
}

func openInput(path string, directory bool) (_ *guardedInput, result error) {
	volume := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(volume) != 2 ||
		volume[1] != ':' || strings.Contains(strings.TrimPrefix(path, volume), ":") {
		return nil, errors.New("unqualified input path")
	}
	rootName, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || inputDriveType(rootName) != windows.DRIVE_FIXED {
		return nil, errors.New("input is not a local fixed drive")
	}
	input := &guardedInput{path: path, directory: directory}
	defer func() {
		if result != nil {
			closeErr := input.close()
			status, nativeStatus := result.(windows.NTStatus) //nolint:errorlint // Only direct native errors qualify; wrappers may carry cleanup failures.
			if closeErr == nil && nativeStatus && errors.Is(status.Errno(), os.ErrNotExist) {
				result = status.Errno()
			} else {
				result = errors.Join(result, closeErr)
			}
		}
	}()
	rootHandle, err := inputWinRoot(rootName, windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_OPEN_NO_RECALL, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(rootHandle), volume+`\`)
	input.files = append(input.files, root)
	signature, err := inputSignature(root, true)
	if err != nil {
		return nil, err
	}
	input.stamps = append(input.stamps, signature)
	relative := strings.TrimPrefix(strings.TrimPrefix(path, volume), `\`)
	if relative == "" {
		if !directory {
			return nil, errors.New("directory is not a source file")
		}
		return input, nil
	}
	for i, part := range strings.Split(relative, `\`) {
		isDirectory := directory || i < len(strings.Split(relative, `\`))-1
		name, err := windows.NewNTUnicodeString(part)
		if err != nil {
			return nil, err
		}
		attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(input.file().Fd()),
			ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
		attributes.Length = uint32(unsafe.Sizeof(attributes))
		options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_OPEN_NO_RECALL | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		share := uint32(windows.FILE_SHARE_READ)
		if isDirectory {
			share |= windows.FILE_SHARE_WRITE
		} else {
			options |= windows.FILE_NON_DIRECTORY_FILE
		}
		var metadataHandle windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = inputNTCreate(&metadataHandle, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
			&attributes, &status, nil, 0, share, windows.FILE_OPEN, options, 0, 0)
		if err != nil {
			return nil, err
		}
		metadata := os.NewFile(uintptr(metadataHandle), part)
		signature, metadataErr := inputSignature(metadata, isDirectory)
		if metadataErr != nil {
			return nil, errors.Join(metadataErr, jsonClose(metadata))
		}
		var dataHandle windows.Handle
		err = inputNTCreate(&dataHandle, windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_DATA|windows.SYNCHRONIZE,
			&attributes, &status, nil, 0, share, windows.FILE_OPEN, options, 0, 0)
		metadataClose := jsonClose(metadata)
		if err != nil {
			if metadataClose == nil {
				return nil, err
			}
			return nil, errors.Join(err, metadataClose)
		}
		file := os.NewFile(uintptr(dataHandle), part)
		input.files = append(input.files, file)
		boundSignature, boundErr := inputSignature(file, isDirectory)
		if metadataClose != nil || boundErr != nil || boundSignature != signature {
			return nil, errors.Join(errors.New("input changed before data access"), metadataClose, boundErr)
		}
		input.stamps = append(input.stamps, signature)
	}
	return input, nil
}
