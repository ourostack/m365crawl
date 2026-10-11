//go:build windows

package officecapture

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDirectoryAdmissionKeepsNoRecallNoReparseMetadataBarrier(t *testing.T) {
	path, _ := ordinaryFixture(t)
	create := inputNTCreate
	t.Cleanup(func() { inputNTCreate = create })
	opens := 0
	inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
		status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
		opens++
		component := attributes.ObjectName.String()
		if attributes.RootDirectory == 0 || component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `\/:`) || options&windows.FILE_DIRECTORY_FILE != 0 ||
			options&windows.FILE_OPEN_REPARSE_POINT == 0 || options&windows.FILE_OPEN_NO_RECALL == 0 ||
			attributes.Attributes&windows.OBJ_DONT_REPARSE == 0 || share&windows.FILE_SHARE_DELETE != 0 {
			t.Fatal("native directory admission weakened held/type/no-recall/no-reparse/share barriers")
		}
		return create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
	}
	if err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
		func(c *JSONCapture) error { _, err := c.ReadJSON(context.Background()); return err }); err != nil || opens == 0 {
		t.Fatal("compatible production primitive refused ordinary synthetic capture")
	}
}

func TestWindowsRegularAncestorRefusedBeforeDataAccess(t *testing.T) {
	path, _ := ordinaryFixture(t)
	create := inputNTCreate
	t.Cleanup(func() { inputNTCreate = create })
	leaf := filepath.Base(path)
	boundaryMetadata, boundaryData := 0, 0
	inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
		status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
		if attributes.ObjectName.String() == leaf {
			if access&windows.FILE_READ_DATA == 0 {
				boundaryMetadata++
			} else {
				boundaryData++
			}
		}
		return create(handle, access, attributes, status, size, fileAttributes, share, disposition, options, ea, eaSize)
	}
	_, err := ObserveJSON(context.Background(), path+`\child.json`)
	var coded *ReadError
	if !errors.As(err, &coded) || coded.Code != "office_json_input_unqualified" ||
		boundaryMetadata != 1 || boundaryData != 0 {
		t.Fatal("regular ancestor gained data access before directory type qualification")
	}
}
