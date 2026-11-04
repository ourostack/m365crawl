//go:build windows

package fileprivacy

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func ValidateACLInAllocation(unsafe.Pointer, uint64, uintptr, *windows.SID, *windows.SID, bool, bool) bool {
	return false
}
