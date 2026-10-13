//go:build windows

package officecapture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Finite directory policy for anchored metadata, not observed maxima.
const (
	directoryBufferBytes = 64 << 10
	directoryEntryLimit  = 4096
	directoryNameBytes   = 1 << 20
	// FILE_ID_BOTH_DIR_INFO: fixed fields end at FileName, offset 104 (FileId at 96).
	directoryRecordHeader = 104
)

var registryDirectoryInfo = windows.GetFileInformationByHandleEx

type directoryEntry struct {
	name       string
	id         uint64
	attributes uint32
	size       int64
	lastWrite  int64
}

// decodeDirectoryInfo decodes one FILE_ID_BOTH_DIR_INFO buffer with explicit checked offsets.
func decodeDirectoryInfo(buffer []byte) ([]directoryEntry, int, error) {
	var out []directoryEntry
	decoded := 0
	for offset := 0; ; {
		if offset < 0 || offset > len(buffer)-directoryRecordHeader {
			return nil, 0, errors.New("directory record outside buffer")
		}
		record := buffer[offset:]
		next := binary.LittleEndian.Uint32(record[0:])
		nameLength := binary.LittleEndian.Uint32(record[60:])
		end := uint64(directoryRecordHeader) + uint64(nameLength)
		if nameLength == 0 || nameLength%2 != 0 || end > uint64(len(record)) ||
			(next != 0 && (uint64(next) < end || uint64(next) > uint64(len(record)) || next%8 != 0)) {
			return nil, 0, errors.New("malformed directory record")
		}
		units := make([]uint16, nameLength/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(record[directoryRecordHeader+2*i:])
		}
		runes := utf16.Decode(units)
		if !slices.Equal(utf16.Encode(runes), units) || slices.Contains(runes, 0xFFFD) {
			return nil, 0, errors.New("malformed directory name")
		}
		entry := directoryEntry{name: string(runes), id: binary.LittleEndian.Uint64(record[96:]),
			attributes: binary.LittleEndian.Uint32(record[56:]), size: int64(binary.LittleEndian.Uint64(record[40:])), //nolint:gosec // native signed LARGE_INTEGER
			lastWrite: int64(binary.LittleEndian.Uint64(record[24:]))} //nolint:gosec // native signed LARGE_INTEGER
		if entry.id == 0 {
			return nil, 0, errors.New("unavailable directory identity")
		}
		out = append(out, entry)
		decoded += len(entry.name)
		if next == 0 {
			return out, decoded, nil
		}
		offset += int(next)
	}
}

// listDirectory enumerates one held directory handle to ERROR_NO_MORE_FILES, so a late matching
// replacement or companion cannot hide after an earlier match.
func listDirectory(ctx context.Context, handle windows.Handle) ([]directoryEntry, error) {
	buffer := make([]byte, directoryBufferBytes)
	class := uint32(windows.FileIdBothDirectoryRestartInfo)
	var entries []directoryEntry
	names := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clear(buffer)
		err := registryDirectoryInfo(handle, class, &buffer[0], directoryBufferBytes)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		class = windows.FileIdBothDirectoryInfo
		batch, decoded, err := decodeDirectoryInfo(buffer)
		if err != nil {
			return nil, err
		}
		entries = append(entries, batch...)
		names += decoded
		if len(entries) > directoryEntryLimit || names > directoryNameBytes {
			return nil, errors.New("directory overflow")
		}
	}
}

// exactEntry returns the one entry whose long name is exactly name. Any other case-insensitive
// spelling of the same name is ambiguity.
func exactEntry(entries []directoryEntry, name string) (directoryEntry, bool, error) {
	var found directoryEntry
	exact, folded := 0, 0
	for _, entry := range entries {
		if strings.EqualFold(entry.name, name) {
			folded++
			if entry.name == name {
				exact++
				found = entry
			}
		}
	}
	if folded > 1 || exact != folded {
		return directoryEntry{}, false, errors.New("ambiguous directory entry")
	}
	return found, exact == 1, nil
}

func (s *guardedInput) sourceParts() ([]string, error) {
	volume := filepath.VolumeName(s.path)
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(s.path, volume), `\`), `\`)
	if s.directory || len(parts) != len(s.files)-1 || len(s.files) != len(s.stamps) {
		return nil, errors.New("held input is not a bound source file")
	}
	return parts, nil
}

// validateSQLite is the lock-safe validation for a source under SQLite SHARED protection. It
// queries held handles and enumerates each held parent's directory metadata; it never opens the
// source again.
func (s *guardedInput) validateSQLite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parts, err := s.sourceParts()
	if err != nil {
		return err
	}
	for i, f := range s.files {
		leaf := i == len(s.files)-1
		signature, err := inputSignature(f, !leaf)
		if err != nil || signature != s.stamps[i] {
			return errors.New("held input changed")
		}
		if i == 0 {
			continue
		}
		var held windows.ByHandleFileInformation
		if err := inputWinInfo(windows.Handle(f.Fd()), &held); err != nil {
			return err
		}
		entries, err := listDirectory(ctx, windows.Handle(s.files[i-1].Fd()))
		if err != nil {
			return err
		}
		entry, found, err := exactEntry(entries, parts[i-1])
		if err != nil || !found {
			return errors.Join(errors.New("held edge missing"), err)
		}
		id := uint64(held.FileIndexHigh)<<32 | uint64(held.FileIndexLow)
		if id == 0 || entry.id != id || entry.attributes != held.FileAttributes {
			return errors.New("held edge changed")
		}
		size := int64(held.FileSizeHigh)<<32 | int64(held.FileSizeLow)
		lastWrite := int64(held.LastWriteTime.HighDateTime)<<32 | int64(held.LastWriteTime.LowDateTime)
		if leaf && (held.NumberOfLinks != 1 || entry.size != size || entry.lastWrite != lastWrite) {
			return errors.New("held source changed or is linked")
		}
	}
	return nil
}

// registryCompanions inspects only the named SQLite companions in the held parent's directory
// metadata. Absent or ordinary zero-length journal/WAL files are retained as exact metadata.
func registryCompanions(ctx context.Context, s *guardedInput) (string, error) {
	parts, err := s.sourceParts()
	if err != nil {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	entries, err := listDirectory(ctx, windows.Handle(s.files[len(s.files)-2].Fd()))
	if err != nil {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	leaf := parts[len(parts)-1]
	var out []string
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		entry, found, err := exactEntry(entries, leaf+suffix)
		if err != nil {
			return "", registryError(ctx, "office_registry_input_unqualified")
		}
		if !found {
			out = append(out, suffix+":absent")
			continue
		}
		if suffix == "-shm" || entry.attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
			return "", registryError(ctx, "office_registry_input_unqualified")
		}
		if entry.size != 0 && suffix == "-journal" {
			return "", registryError(ctx, "office_registry_source_busy")
		}
		if entry.size != 0 {
			return "", registryError(ctx, "office_registry_input_unqualified")
		}
		out = append(out, fmt.Sprintf("%s:%d:%d:%d:%d", suffix, entry.id, entry.attributes, entry.size, entry.lastWrite))
	}
	return strings.Join(out, "|"), nil
}
