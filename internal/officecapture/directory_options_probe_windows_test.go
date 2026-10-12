//go:build windows

package officecapture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsInputQualificationDirectoryOptionProbe(t *testing.T) {
	path, _ := ordinaryFixture(t)
	create := inputNTCreate
	t.Cleanup(func() { inputNTCreate = create })
	inputNTCreate = func(handle *windows.Handle, access uint32, attributes *windows.OBJECT_ATTRIBUTES,
		status *windows.IO_STATUS_BLOCK, size *int64, fileAttributes, share, disposition, options uint32, ea uintptr, eaSize uint32) error {
		component := attributes.ObjectName.String()
		if attributes.RootDirectory == 0 || component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `\/:`) ||
			options&windows.FILE_OPEN_REPARSE_POINT == 0 || options&windows.FILE_OPEN_NO_RECALL == 0 {
			t.Fatal("directory probe escaped held single-component/no-reparse/no-recall boundary")
		}
		adjusted := options &^ uint32(windows.FILE_DIRECTORY_FILE)
		t.Logf("directory-option-probe attributes-length=%d structure-bytes=%d name-bytes=%d access=0x%08x object-flags=0x%08x options=0x%08x adjusted=0x%08x file-attributes=0x%08x share=0x%08x disposition=%d io-status-bytes=%d",
			attributes.Length, unsafe.Sizeof(*attributes), attributes.ObjectName.Length, access,
			attributes.Attributes, options, adjusted, fileAttributes, share, disposition, unsafe.Sizeof(*status))
		err := create(handle, access, attributes, status, size, fileAttributes, share, disposition, adjusted, ea, eaSize)
		if err != nil {
			var code windows.NTStatus
			if errors.As(err, &code) {
				t.Logf("directory-option-probe ntstatus=0x%08x io-status=0x%08x", uint32(code), uint32(status.Status))
			}
		}
		return err
	}
	consumed := false
	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
		func(c *JSONCapture) error {
			result, err := c.ReadJSON(context.Background())
			consumed = err == nil && len(result.Documents) == 1 && result.Documents[0].Title == "Synthetic document"
			return err
		})
	if err != nil || !consumed {
		t.Fatal("single directory-option hypothesis refused ordinary fixture")
	}
}
