//go:build windows

package officecapture

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// nativeIDBothDirInfo mirrors FILE_ID_BOTH_DIR_INFO for the ABI check only.
type nativeIDBothDirInfo struct {
	NextEntryOffset uint32
	FileIndex       uint32
	CreationTime    int64
	LastAccessTime  int64
	LastWriteTime   int64
	ChangeTime      int64
	EndOfFile       int64
	AllocationSize  int64
	FileAttributes  uint32
	FileNameLength  uint32
	EaSize          uint32
	ShortNameLength int8
	ShortName       [12]uint16
	FileID          int64
	FileName        [1]uint16
}

// leafCloseBeforeBound is how many handles named like the leaf an open closes before the bound
// one: the metadata-only handle that is compared with the data handle and then closed.
const leafCloseBeforeBound = 1

// heldSourceDeniesWrite reports whether err is the sharing violation a held source handle, which
// shares read access only, gives a writer.
func heldSourceDeniesWrite(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }

// openDescriptors is not counted on Windows: the runtime's own handle count is not stable enough
// to compare. The lock and writer checks still run.
func openDescriptors(*testing.T) int { return 0 }

func TestRegistryDirectoryInfoABI(t *testing.T) {
	var info nativeIDBothDirInfo
	for name, got := range map[string]uintptr{
		"LastWriteTime": unsafe.Offsetof(info.LastWriteTime), "EndOfFile": unsafe.Offsetof(info.EndOfFile),
		"FileAttributes": unsafe.Offsetof(info.FileAttributes), "FileNameLength": unsafe.Offsetof(info.FileNameLength),
		"FileId": unsafe.Offsetof(info.FileID), "FileName": unsafe.Offsetof(info.FileName),
	} {
		want := map[string]uintptr{"LastWriteTime": 24, "EndOfFile": 40, "FileAttributes": 56, "FileNameLength": 60, "FileId": 96, "FileName": directoryRecordHeader}[name]
		if got != want {
			t.Fatalf("%s offset %d, decoder uses %d", name, got, want)
		}
	}
}

func TestRegistryDirectoryListingMatchesHeldIdentity(t *testing.T) {
	path := registrySource(t)
	input, err := openInput(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.close() }()
	entries, err := listDirectory(context.Background(), windows.Handle(input.files[len(input.files)-2].Fd()))
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := exactEntry(entries, filepath.Base(path))
	var held windows.ByHandleFileInformation
	if infoErr := windows.GetFileInformationByHandle(windows.Handle(input.file().Fd()), &held); infoErr != nil {
		t.Fatal(infoErr)
	}
	if err != nil || !found || entry.id != uint64(held.FileIndexHigh)<<32|uint64(held.FileIndexLow) {
		t.Fatalf("directory identity does not match held handle: %v %t", err, found)
	}
	if err := input.validateSQLite(context.Background()); err != nil {
		t.Fatalf("held source failed anchored validation: %v", err)
	}
}

func directoryRecord(name string, id uint64, next uint32) []byte {
	units := utf16.Encode([]rune(name))
	record := make([]byte, directoryRecordHeader+2*len(units))
	binary.LittleEndian.PutUint32(record[0:], next)
	binary.LittleEndian.PutUint32(record[60:], uint32(2*len(units))) //nolint:gosec // short synthetic name
	binary.LittleEndian.PutUint64(record[96:], id)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(record[directoryRecordHeader+2*i:], unit)
	}
	return record
}

func TestRegistryDirectoryDecodeRefusals(t *testing.T) {
	good := directoryRecord("a.reg", 7, 0)
	if entries, _, err := decodeDirectoryInfo(good); err != nil || len(entries) != 1 || entries[0].name != "a.reg" {
		t.Fatalf("valid record refused: %v", err)
	}
	bad := map[string][]byte{
		"short":      good[:directoryRecordHeader-1],
		"zero-id":    directoryRecord("a.reg", 0, 0),
		"next-short": directoryRecord("a.reg", 7, 8),
		"next-past":  directoryRecord("a.reg", 7, 4096),
		"surrogate":  directoryRecord("a", 7, 0),
	}
	binary.LittleEndian.PutUint16(bad["surrogate"][directoryRecordHeader:], 0xD800)
	odd := directoryRecord("a.reg", 7, 0)
	binary.LittleEndian.PutUint32(odd[60:], 3)
	bad["odd-length"] = odd
	for name, buffer := range bad {
		if _, _, err := decodeDirectoryInfo(buffer); err == nil {
			t.Fatalf("%s directory record accepted", name)
		}
	}
	if _, _, err := exactEntry([]directoryEntry{{name: "A.reg", id: 1}, {name: "a.reg", id: 2}}, "a.reg"); err == nil {
		t.Fatal("case-ambiguous directory edge accepted")
	}
}

func TestRegistryDirectoryEnumerationFaults(t *testing.T) {
	path := registrySource(t)
	original := registryDirectoryInfo
	t.Cleanup(func() { registryDirectoryInfo = original })
	registryDirectoryInfo = func(windows.Handle, uint32, *byte, uint32) error { return errors.New("enumeration fault") }
	if _, err := ObserveRegistry(context.Background(), path); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatalf("enumeration fault became evidence: %v", err)
	}
	calls := 0
	registryDirectoryInfo = func(handle windows.Handle, class uint32, buffer *byte, size uint32) error {
		calls++
		if calls > directoryEntryLimit+2 {
			return windows.ERROR_NO_MORE_FILES
		}
		record := directoryRecord("x", uint64(calls), 0) //nolint:gosec // bounded synthetic count
		copy(unsafe.Slice(buffer, size), record)         //nolint:gosec // test stand-in for the native call filling the caller buffer
		return nil
	}
	if _, err := listDirectory(context.Background(), 0); err == nil {
		t.Fatal("directory overflow accepted")
	}
}

// patchLeafRecord flips one byte of the named entry's directory record after the native call.
func patchLeafRecord(t *testing.T, leaf string, offset int) {
	t.Helper()
	original := registryDirectoryInfo
	t.Cleanup(func() { registryDirectoryInfo = original })
	registryDirectoryInfo = func(handle windows.Handle, class uint32, buffer *byte, size uint32) error {
		if err := original(handle, class, buffer, size); err != nil {
			return err
		}
		records := unsafe.Slice(buffer, size) //nolint:gosec // the caller's own buffer
		for at := 0; at+directoryRecordHeader <= len(records); {
			next := int(binary.LittleEndian.Uint32(records[at:]))
			length := int(binary.LittleEndian.Uint32(records[at+60:]))
			if at+directoryRecordHeader+length > len(records) {
				break
			}
			units := make([]uint16, length/2)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(records[at+directoryRecordHeader+2*i:])
			}
			if string(utf16.Decode(units)) == leaf {
				records[at+offset] ^= 1
			}
			if next == 0 {
				break
			}
			at += next
		}
		return nil
	}
}

// Each anchored leaf comparison must fail alone: the directory entry's identity, attributes, size
// and last-write time are each compared with the held handle.
func TestRegistryLeafDirectoryEntryIsolated(t *testing.T) {
	for name, offset := range map[string]int{"identity": 96, "attributes": 56, "size": 40, "last-write": 24} {
		t.Run(name, func(t *testing.T) {
			path := registrySource(t)
			input, err := openInput(path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.close() }()
			if err := input.validateSQLite(context.Background()); err != nil {
				t.Fatalf("unchanged held source refused: %v", err)
			}
			patchLeafRecord(t, filepath.Base(path), offset)
			if err := input.validateSQLite(context.Background()); err == nil {
				t.Fatalf("a directory entry whose %s differs from the held handle passed", name)
			}
		})
	}
}

func TestRegistryLockSafeValidationRefusalsOnWindows(t *testing.T) {
	path := registrySource(t)
	input, err := openInput(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.close() }()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := input.validateSQLite(cancelled); err == nil {
		t.Fatal("cancelled validation passed")
	}
	directory, err := openInput(filepath.Dir(path), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.close() }()
	if err := directory.validateSQLite(context.Background()); err == nil {
		t.Fatal("directory accepted as a bound source")
	}
	if _, err := registryCompanions(context.Background(), directory); registryCode(err) != "office_registry_input_unqualified" {
		t.Fatal("directory companions inspected")
	}
	stamp := input.stamps[1]
	input.stamps[1] = "changed"
	if err := input.validateSQLite(context.Background()); err == nil {
		t.Fatal("changed held stamp passed")
	}
	input.stamps[1] = stamp
	// A supplied path must use the directory entry's exact long name: another case is refused.
	parts, err := input.sourceParts()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := listDirectory(context.Background(), windows.Handle(input.files[len(input.files)-2].Fd()))
	if err != nil {
		t.Fatal(err)
	}
	leaf := parts[len(parts)-1]
	if _, found, err := exactEntry(entries, "MICROSOFTREGISTRATIONDB_SYNTHETIC.REG"); found || err == nil {
		t.Fatalf("a differently cased name matched %s: %t %v", leaf, found, err)
	}
	if _, found, err := exactEntry(entries, leaf+".absent"); found || err != nil {
		t.Fatalf("absent name: %t %v", found, err)
	}
}
