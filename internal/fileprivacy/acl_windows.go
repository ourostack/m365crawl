//go:build windows

package fileprivacy

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ValidateACLInAllocation(base unsafe.Pointer, size uint64, address uintptr, user, system *windows.SID, directory, requireFileInheritance bool) bool {
	origin := uintptr(base)
	if size < 8 || size > uint64(^uint(0)>>1) || address < origin || uint64(address-origin) > size-8 {
		return false
	}
	data := unsafe.Slice((*byte)(base), int(size))
	start := int(address - origin)
	length := int(binary.LittleEndian.Uint16(data[start+2 : start+4]))
	if length < 8 || length > len(data)-start {
		return false
	}
	return ValidateACL(data[start:start+length], user, system, directory, requireFileInheritance)
}

func ValidateACL(data []byte, user, system *windows.SID, directory, requireFileInheritance bool) bool {
	if len(data) < 8 || len(data)%4 != 0 || len(data) != int(binary.LittleEndian.Uint16(data[2:4])) ||
		(data[0] != 2 && data[0] != 4) || data[1] != 0 || binary.LittleEndian.Uint16(data[6:8]) != 0 ||
		user == nil || system == nil || !user.IsValid() || !system.IsValid() {
		return false
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 || count > (len(data)-8)/16 {
		return false
	}
	userAllowed, systemAllowed, userInherited, systemInherited := false, false, false, false
	offset := 8
	for i := 0; i < count; i++ {
		if len(data)-offset < 16 {
			return false
		}
		length := int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
		if length < 16 || length%4 != 0 || length > len(data)-offset {
			return false
		}
		ace := data[offset : offset+length]
		offset += length
		flags := ace[1]
		const inherit = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		if ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE ||
			flags & ^uint8(inherit|windows.INHERIT_ONLY_ACE|windows.INHERITED_ACE) != 0 ||
			(!directory && flags & ^uint8(windows.INHERITED_ACE) != 0) ||
			(flags&windows.INHERIT_ONLY_ACE != 0 && flags&inherit == 0) {
			return false
		}
		mask := binary.LittleEndian.Uint32(ace[4:8])
		if mask != windows.GENERIC_ALL && mask != 0x1f01ff {
			return false
		}
		if ace[9] > 15 || len(ace)-8 != 8+4*int(ace[9]) {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace[8]))
		if ace[8] != 1 || !sid.IsValid() {
			return false
		}
		isUser, isSystem := sid.Equals(user), sid.Equals(system)
		if !isUser && !isSystem {
			return false
		}
		if flags&windows.INHERIT_ONLY_ACE == 0 {
			userAllowed = userAllowed || isUser
			systemAllowed = systemAllowed || isSystem
		}
		if flags&windows.OBJECT_INHERIT_ACE != 0 {
			userInherited = userInherited || isUser
			systemInherited = systemInherited || isSystem
		}
	}
	for _, spare := range data[offset:] {
		if spare != 0 {
			return false
		}
	}
	return userAllowed && systemAllowed && (!requireFileInheritance || userInherited && systemInherited)
}
