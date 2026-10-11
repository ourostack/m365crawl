//go:build windows

package officecapture

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsInputQualificationSingleComponentParameterProbe(t *testing.T) {
	path, _ := ordinaryFixture(t)
	create := inputNTCreate
	t.Cleanup(func() { inputNTCreate = create })
	inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
		status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
		component := attributes.ObjectName.String()
		if attributes.RootDirectory == 0 || component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `\/:`) ||
			options&windows.FILE_OPEN_REPARSE_POINT == 0 || options&windows.FILE_OPEN_NO_RECALL == 0 {
			t.Fatal("parameter probe escaped held single-component/no-follow/no-recall boundary")
		}
		bound := *attributes
		bound.Attributes &^= windows.OBJ_DONT_REPARSE
		err := create(handle, access, &bound, status, size, fileAttributes, share, disposition, options, ea, eaSize)
		if err != nil {
			if code, ok := err.(windows.NTStatus); ok {
				t.Logf("single-component-without-optional-attribute ntstatus=0x%08x", uint32(code))
			}
		}
		return err
	}
	var consumed bool
	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(c *JSONCapture) error {
		result, err := c.ReadJSON(context.Background())
		if err == nil && len(result.Documents) == 1 && result.Documents[0].Title == "Synthetic document" {
			consumed = true
		}
		return err
	})
	if err != nil || !consumed {
		t.Fatal("single-parameter native hypothesis refused ordinary fixture")
	}
}
