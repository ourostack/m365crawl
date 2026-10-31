package scoutcapture

import (
	"encoding/binary"
	"testing"
	"time"
	"unicode/utf16"
)

// Attribute values observed on OneDrive placeholders and folders; synthetic, no real names.
const (
	testCloudOnlyAttrs = 0x00401600 // OFFLINE | RECALL_ON_DATA_ACCESS | REPARSE_POINT | SPARSE
	testCloudFileTag   = 0x9000401A
	testCloudDirTag    = 0x9000601A
)

func TestWinFileStateFollowsAttributes(t *testing.T) {
	const max = 100
	cases := []struct {
		name   string
		entry  winEntry
		state  string
		reason string
	}{
		{"plain file", winEntry{Attributes: winAttrArchive, Size: 10}, "", ""},
		{"normal attribute", winEntry{Attributes: winAttrNormal, Size: 10}, "", ""},
		{"hydrated cloud file", winEntry{Attributes: winAttrArchive | winAttrReparsePoint | winAttrPinned, Tag: testCloudFileTag, Size: 10}, "", ""},
		{"cloud-only placeholder", winEntry{Attributes: testCloudOnlyAttrs, Tag: testCloudFileTag, Size: 10}, StateCloudOnly, "scout_file_cloud_only"},
		{"offline", winEntry{Attributes: winAttrOffline, Size: 10}, StateCloudOnly, "scout_file_cloud_only"},
		{"recall on open", winEntry{Attributes: winAttrRecallOnOpen, Size: 10}, StateCloudOnly, "scout_file_cloud_only"},
		{"recall on data access", winEntry{Attributes: winAttrRecallOnDataAccess, Size: 10}, StateCloudOnly, "scout_file_cloud_only"},
		{"partly downloaded", winEntry{Attributes: winAttrReparsePoint | winAttrSparse, Tag: testCloudFileTag, Size: 10}, StateCloudOnly, "scout_file_cloud_only"},
		{"directory", winEntry{Attributes: winAttrDirectory}, StateUnsafe, "scout_file_unsafe"},
		{"symbolic link", winEntry{Attributes: winAttrReparsePoint, Tag: 0xA000000C, Size: 10}, StateUnsafe, "scout_file_unsafe"},
		{"junction-tagged file", winEntry{Attributes: winAttrReparsePoint, Tag: 0xA0000003}, StateUnsafe, "scout_file_unsafe"},
		{"device", winEntry{Attributes: winAttrDevice}, StateUnsafe, "scout_file_unsafe"},
		{"virtual", winEntry{Attributes: winAttrVirtual}, StateUnsafe, "scout_file_unsafe"},
		{"unsafe wins over cloud-only", winEntry{Attributes: winAttrReparsePoint | winAttrOffline, Tag: 0xA000000C}, StateUnsafe, "scout_file_unsafe"},
		{"too large", winEntry{Attributes: winAttrArchive, Size: max + 1}, StateTooLarge, "scout_file_too_large"},
		{"cloud-only wins over too large", winEntry{Attributes: testCloudOnlyAttrs, Tag: testCloudFileTag, Size: max + 1}, StateCloudOnly, "scout_file_cloud_only"},
	}
	for _, c := range cases {
		state, reason := winFileState(c.entry, max)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: %q %q, want %q %q", c.name, state, reason, c.state, c.reason)
		}
	}
}

func TestWinDirReadable(t *testing.T) {
	cases := []struct {
		name  string
		attrs uint32
		tag   uint32
		want  bool
	}{
		{"plain folder", winAttrDirectory, 0, true},
		{"OneDrive folder", winAttrDirectory | winAttrReparsePoint, testCloudDirTag, true},
		{"pinned OneDrive folder", winAttrDirectory | winAttrReparsePoint | winAttrPinned, testCloudDirTag, true},
		{"not a folder", winAttrArchive, 0, false},
		{"junction", winAttrDirectory | winAttrReparsePoint, 0xA0000003, false},
		{"offline folder", winAttrDirectory | winAttrOffline, 0, false},
		{"folder to be fetched on listing", winAttrDirectory | winAttrRecallOnOpen, 0, false},
	}
	for _, c := range cases {
		if got := winDirReadable(c.attrs, c.tag); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestWinCloudTagAcceptsEveryCloudVariant(t *testing.T) {
	for _, tag := range []uint32{0x9000001A, 0x9000101A, 0x9000401A, 0x9000601A, 0x9000F01A} {
		if !winCloudTag(tag) {
			t.Errorf("%#x refused", tag)
		}
	}
	for _, tag := range []uint32{0, 0xA000000C, 0xA0000003, 0x8000001B, 0x9000001B, 0x9001001A} {
		if winCloudTag(tag) {
			t.Errorf("%#x accepted", tag)
		}
	}
}

func TestWinTimeConvertsFileTime(t *testing.T) {
	want := time.Date(2026, 10, 31, 4, 8, 58, 123456700, time.UTC)
	ticks := want.UnixNano()/100 + 116444736000000000
	if got := winTime(ticks); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("%v", got)
	}
}

// dirRecord builds one FILE_ID_BOTH_DIR_INFO record padded to 8 bytes.
func dirRecord(name string, attrs, eaSize uint32, size, writeTicks int64, last bool) []byte {
	units := utf16.Encode([]rune(name))
	length := winDirNameOffset + 2*len(units)
	padded := (length + 7) &^ 7
	b := make([]byte, padded)
	if !last {
		binary.LittleEndian.PutUint32(b[0:], uint32(padded))
	}
	binary.LittleEndian.PutUint64(b[24:], uint64(writeTicks))
	binary.LittleEndian.PutUint64(b[40:], uint64(size))
	binary.LittleEndian.PutUint32(b[56:], attrs)
	binary.LittleEndian.PutUint32(b[60:], uint32(2*len(units)))
	binary.LittleEndian.PutUint32(b[64:], eaSize)
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[winDirNameOffset+2*i:], u)
	}
	return b
}

func TestParseWinDirInfo(t *testing.T) {
	ticks := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano()/100 + 116444736000000000
	var buf []byte
	buf = append(buf, dirRecord(".", winAttrDirectory, 0, 0, ticks, false)...)
	buf = append(buf, dirRecord("..", winAttrDirectory, 0, 0, ticks, false)...)
	buf = append(buf, dirRecord("a-1.json", testCloudOnlyAttrs, testCloudFileTag, 2048, ticks, false)...)
	buf = append(buf, dirRecord("b.json", winAttrArchive, 77, 9, ticks, true)...)
	entries, err := parseWinDirInfo(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%+v", entries)
	}
	first, second := entries[0], entries[1]
	if first.Name != "a-1.json" || first.Attributes != testCloudOnlyAttrs || first.Tag != testCloudFileTag || first.Size != 2048 || !first.Modified.Equal(winTime(ticks)) {
		t.Fatalf("first: %+v", first)
	}
	// EaSize holds the reparse tag only for reparse points.
	if second.Name != "b.json" || second.Tag != 0 || second.Size != 9 {
		t.Fatalf("second: %+v", second)
	}
}

func TestParseWinDirInfoRefusesMalformedBuffers(t *testing.T) {
	good := dirRecord("x.json", winAttrArchive, 0, 1, 1, true)
	short := dirRecord("x.json", winAttrArchive, 0, 1, 1, true)[:winDirNameOffset-1]
	nameOverrun := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(nameOverrun[60:], 4096)
	oddName := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(oddName[60:], 3)
	backwards := dirRecord("x.json", winAttrArchive, 0, 1, 1, false)
	binary.LittleEndian.PutUint32(backwards[0:], 4)
	pastEnd := dirRecord("x.json", winAttrArchive, 0, 1, 1, false)
	binary.LittleEndian.PutUint32(pastEnd[0:], 4096)
	negative := dirRecord("x.json", winAttrArchive, 0, -1, 1, true)
	noName := make([]byte, winDirNameOffset)
	for name, buf := range map[string][]byte{
		"empty": nil, "short": short, "name overrun": nameOverrun, "odd name": oddName,
		"backwards": backwards, "past end": pastEnd, "negative size": negative, "no name": noName,
	} {
		if _, err := parseWinDirInfo(buf); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWinCloudErrors(t *testing.T) {
	for _, code := range []uint32{358, 362, 377, 395, 405, 426} {
		if !winCloudErrno(code) {
			t.Errorf("errno %d", code)
		}
	}
	for _, code := range []uint32{0, 2, 5, 357, 406, 425, 427} {
		if winCloudErrno(code) {
			t.Errorf("errno %d accepted", code)
		}
	}
	for _, status := range []uint32{0xC000CF00, 0xC000CF01, 0xC000CF1F, 0xC000CFFF} {
		if !winCloudStatus(status) {
			t.Errorf("status %#x", status)
		}
	}
	for _, status := range []uint32{0, 0xC0000034, 0x8000CF00, 0xC000CE00, 0xC001CF00} {
		if winCloudStatus(status) {
			t.Errorf("status %#x accepted", status)
		}
	}
}
