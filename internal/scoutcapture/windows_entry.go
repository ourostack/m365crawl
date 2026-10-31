package scoutcapture

import (
	"encoding/binary"
	"errors"
	"time"
	"unicode/utf16"
)

// Windows file attribute bits and the cloud-file reparse tag, from the Windows SDK. They live in
// a portable file so the decisions below are tested on every platform.
const (
	winAttrDirectory          = 0x00000010
	winAttrArchive            = 0x00000020
	winAttrDevice             = 0x00000040
	winAttrNormal             = 0x00000080
	winAttrSparse             = 0x00000200
	winAttrReparsePoint       = 0x00000400
	winAttrOffline            = 0x00001000
	winAttrVirtual            = 0x00010000
	winAttrRecallOnOpen       = 0x00040000
	winAttrPinned             = 0x00080000
	winAttrRecallOnDataAccess = 0x00400000

	// IO_REPARSE_TAG_CLOUD through IO_REPARSE_TAG_CLOUD_F differ only in bits 12-15.
	winCloudTagValue = 0x9000001A
	winCloudTagMask  = 0xFFFF0FFF

	// FILE_ID_BOTH_DIR_INFO: the name follows a fixed 104-byte header.
	winDirNameOffset = 104
	winEpochTicks    = 116444736000000000
)

// winNotLocal marks an entry whose bytes are not all on this computer: reading it would fetch
// them. A sparse file is included because a partly downloaded OneDrive file is sparse.
const winNotLocal = winAttrOffline | winAttrRecallOnOpen | winAttrRecallOnDataAccess | winAttrSparse

// winEntry is one directory entry as Windows lists it, read without opening the entry.
type winEntry struct {
	Name       string
	Attributes uint32
	Tag        uint32 // the reparse tag when Attributes has the reparse-point bit
	Size       int64
	Modified   time.Time
}

func winCloudTag(tag uint32) bool { return tag&winCloudTagMask == winCloudTagValue }

// winLinkFree is true when the attributes name no reparse point other than a OneDrive cloud
// file or folder, and nothing that is not an ordinary file or folder.
func winLinkFree(attrs, tag uint32) bool {
	if attrs&(winAttrDevice|winAttrVirtual) != 0 {
		return false
	}
	return attrs&winAttrReparsePoint == 0 || winCloudTag(tag)
}

// winDirReadable is true for a folder that can be listed without fetching anything.
func winDirReadable(attrs, tag uint32) bool {
	return attrs&winAttrDirectory != 0 && winLinkFree(attrs, tag) && attrs&winNotLocal == 0
}

// winFileState decides, from a listing alone, whether a session file may be opened: it returns
// an empty state when it may, and otherwise the state and reason to report without opening it.
func winFileState(e winEntry, maxBytes int64) (string, string) {
	switch {
	case e.Attributes&winAttrDirectory != 0 || !winLinkFree(e.Attributes, e.Tag):
		return StateUnsafe, "scout_file_unsafe"
	case e.Attributes&winNotLocal != 0:
		return StateCloudOnly, "scout_file_cloud_only"
	case e.Size > maxBytes:
		return StateTooLarge, "scout_file_too_large"
	}
	return "", ""
}

// winTime converts a FILETIME count of 100-nanosecond ticks since 1601 to UTC.
func winTime(ticks int64) time.Time {
	return time.Unix(0, (ticks-winEpochTicks)*100).UTC()
}

var errWinDirInfo = errors.New("malformed directory listing")

// parseWinDirInfo decodes a chain of FILE_ID_BOTH_DIR_INFO records, skipping "." and "..".
func parseWinDirInfo(buf []byte) ([]winEntry, error) {
	if len(buf) == 0 {
		return nil, errWinDirInfo
	}
	out := []winEntry{}
	for offset := 0; ; {
		if len(buf)-offset < winDirNameOffset {
			return nil, errWinDirInfo
		}
		rec := buf[offset:]
		next := int(binary.LittleEndian.Uint32(rec[0:]))
		nameBytes := int(binary.LittleEndian.Uint32(rec[60:]))
		size := int64(binary.LittleEndian.Uint64(rec[40:])) //nolint:gosec // G115: a file size Windows reported
		if nameBytes == 0 || nameBytes%2 != 0 || nameBytes > len(rec)-winDirNameOffset || size < 0 ||
			(next != 0 && (next < winDirNameOffset+nameBytes || next > len(rec))) {
			return nil, errWinDirInfo
		}
		units := make([]uint16, nameBytes/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(rec[winDirNameOffset+2*i:])
		}
		e := winEntry{
			Name:       string(utf16.Decode(units)),
			Attributes: binary.LittleEndian.Uint32(rec[56:]),
			Size:       size,
			Modified:   winTime(int64(binary.LittleEndian.Uint64(rec[24:]))), //nolint:gosec // G115: a FILETIME
		}
		if e.Attributes&winAttrReparsePoint != 0 {
			e.Tag = binary.LittleEndian.Uint32(rec[64:])
		}
		if e.Name != "." && e.Name != ".." {
			out = append(out, e)
		}
		if next == 0 {
			return out, nil
		}
		offset += next
	}
}

// winCloudErrno is true for a Win32 error the cloud files filter returns, such as a refused or
// failed download.
func winCloudErrno(code uint32) bool { return code >= 358 && code <= 405 || code == 426 }

// winCloudStatus is true for an error NTSTATUS from the cloud files filter (0xC000CF00-0xC000CFFF).
func winCloudStatus(status uint32) bool { return status&0xFFFFFF00 == 0xC000CF00 }
