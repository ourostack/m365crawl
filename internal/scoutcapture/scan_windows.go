//go:build windows

package scoutcapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ourostack/m365crawl/internal/scoutsessions"
)

// Seams for tests; production uses the system calls.
var (
	winNTCreate  = windows.NtCreateFile
	winInfoEx    = windows.GetFileInformationByHandleEx
	winInfo      = windows.GetFileInformationByHandle
	winVolume    = windows.GetVolumeInformationByHandle
	winDriveType = windows.GetDriveType
	winOpenRoot  = windows.CreateFile
	winGetenv    = os.Getenv
	winReadAll   = io.ReadAll
	winList      = listDir
	winAttrTag   = attrTag
)

// winOptions open one name relative to an open folder without following any reparse point and
// without asking the cloud files filter to download anything.
const winOptions = windows.FILE_OPEN_REPARSE_POINT | windows.FILE_OPEN_NO_RECALL | windows.FILE_SYNCHRONOUS_IO_NONALERT

// winShare never blocks OneDrive or the app from changing a file while it is read; a change is
// caught by the checks after the read.
const winShare = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// Supported is true: acquisition is qualified on Windows.
func Supported() bool { return true }

// withGuard runs operation. Windows has no per-thread switch that stops OneDrive downloads: the
// guard is that a file is opened only when its listing shows every byte on this computer, every
// open asks for no download, and the attributes are checked again on the open file.
func withGuard(ctx context.Context, operation func() error) (result error) {
	defer func() {
		if recover() != nil {
			result = &Error{Code: "scout_capture_failed"}
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

// Check confirms that root is an app folder with sessions\devices on a local fixed NTFS drive,
// reached without following links, and returns it. It lists and reads nothing.
func Check(ctx context.Context, root string) (string, error) {
	err := withGuard(ctx, func() error {
		devices, err := openLayout(root)
		if err != nil {
			return err
		}
		return devices.Close()
	})
	if err != nil {
		return "", err
	}
	return root, nil
}

// Scan lists every device folder of the app folder at opts.Root and hands each session file to
// consume, in device then file name order. It returns an *Error when the folder cannot be scanned
// at all, consume's error, or the context's error; a device folder or file that cannot be read is
// reported in the result and the scan goes on.
func Scan(ctx context.Context, opts Options, consume func(File) error) (Result, error) {
	var res Result
	err := withGuard(ctx, func() error {
		devices, err := openLayout(opts.Root)
		if err != nil {
			return err
		}
		defer func() { _ = devices.Close() }()
		res.Root = opts.Root
		return scan(ctx, opts, consume, devices, &res)
	})
	return res, err
}

func handleOf(f *os.File) windows.Handle { return windows.Handle(f.Fd()) }

// openChild opens name inside parent. access is added to SYNCHRONIZE. A folder is opened without
// FILE_DIRECTORY_FILE, which Windows refuses next to FILE_OPEN_NO_RECALL; the caller checks the
// folder attribute on the open handle instead.
func openChild(parent windows.Handle, name string, directory bool, access uint32) (*os.File, error) {
	if name == "" || strings.ContainsAny(name, `\/:`) {
		return nil, errors.New("unqualified name")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(winOptions)
	if !directory {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	if err := winNTCreate(&handle, access|windows.SYNCHRONIZE, &attributes, &status, nil, 0, winShare, windows.FILE_OPEN, options, 0, 0); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

// attrTag reads an open file's attributes and reparse tag.
func attrTag(f *os.File) (uint32, uint32, error) {
	var info struct{ Attributes, Tag uint32 }
	err := winInfoEx(handleOf(f), windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))) //nolint:gosec // G103: the documented FILE_ATTRIBUTE_TAG_INFO buffer
	return info.Attributes, info.Tag, err
}

// openFolder opens name inside parent as a folder that can be listed and checks, on the open
// handle, that it is an ordinary or OneDrive folder that is not waiting to be fetched.
func openFolder(parent windows.Handle, name string) (*os.File, error) {
	dir, err := openChild(parent, name, true, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return nil, err
	}
	attrs, tag, err := winAttrTag(dir)
	if err == nil && !winDirReadable(attrs, tag) {
		err = errors.New("unqualified folder")
	}
	if err != nil {
		return nil, errors.Join(err, dir.Close())
	}
	return dir, nil
}

// openPath opens path, a folder on a local fixed NTFS drive, one name at a time from the drive's
// root, refusing links and folders that are not on this computer. It returns the open folder.
func openPath(path string) (_ *os.File, result error) {
	volume := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(volume) != 2 || volume[1] != ':' ||
		strings.Contains(path[len(volume):], ":") {
		return nil, &Error{Code: "scout_root_invalid"}
	}
	rootName, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return nil, &Error{Code: "scout_root_invalid"}
	}
	if winDriveType(rootName) != windows.DRIVE_FIXED {
		return nil, &Error{Code: "scout_root_unqualified"}
	}
	handle, err := winOpenRoot(rootName, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		winShare, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_OPEN_NO_RECALL, 0)
	if err != nil {
		return nil, &Error{Code: "scout_root_invalid"}
	}
	current := os.NewFile(uintptr(handle), volume+`\`)
	defer func() {
		if result != nil {
			_ = current.Close()
		}
	}()
	var fs [32]uint16
	if winVolume(handle, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))) != nil || windows.UTF16ToString(fs[:]) != "NTFS" {
		return nil, &Error{Code: "scout_root_unqualified"}
	}
	for _, name := range strings.Split(strings.TrimPrefix(path[len(volume):], `\`), `\`) {
		if name == "" {
			continue
		}
		next, err := openFolder(handleOf(current), name)
		if err != nil {
			return nil, &Error{Code: "scout_root_invalid"}
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

// openLayout opens root\sessions\devices and returns the open devices folder.
func openLayout(root string) (*os.File, error) {
	return openPath(filepath.Join(root, "sessions", "devices"))
}

// listDir lists an open folder from its handle, without opening any entry.
func listDir(dir *os.File) ([]winEntry, error) {
	words := make([]uint64, 8192)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8) //nolint:gosec // G103: an 8-byte aligned listing buffer
	out := []winEntry{}
	class := uint32(windows.FileIdBothDirectoryRestartInfo)
	for {
		err := winInfoEx(handleOf(dir), class, &buf[0], uint32(len(buf))) //nolint:gosec // G115: 64 KiB
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		entries, err := parseWinDirInfo(buf)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
		class = windows.FileIdBothDirectoryInfo
	}
}

func sortEntries(entries []winEntry) {
	slices.SortFunc(entries, func(a, b winEntry) int { return strings.Compare(a.Name, b.Name) })
}

func scan(ctx context.Context, opts Options, consume func(File) error, devices *os.File, res *Result) error {
	entries, err := winList(devices)
	if err != nil {
		return &Error{Code: "scout_devices_unreadable"}
	}
	sortEntries(entries)
	for _, e := range entries {
		if !scoutsessions.DeviceFolder(e.Name) {
			res.Ignored++
			continue
		}
		device := Device{ID: e.Name}
		err := scanDevice(ctx, devices, e, &device, opts, consume, res)
		res.Devices = append(res.Devices, device)
		if err != nil {
			return err
		}
	}
	return nil
}

func scanDevice(ctx context.Context, devices *os.File, e winEntry, device *Device, opts Options, consume func(File) error, res *Result) error {
	if !winDirReadable(e.Attributes, e.Tag) {
		device.Reason = "scout_device_unreadable"
		return nil
	}
	dir, err := openFolder(handleOf(devices), e.Name)
	if err != nil {
		device.Reason = "scout_device_unreadable"
		return nil
	}
	defer func() { _ = dir.Close() }()
	entries, err := winList(dir)
	if err != nil {
		device.Reason = "scout_device_unreadable"
		return nil
	}
	device.Listed = true
	sortEntries(entries)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scoutsessions.SessionFile(entry.Name) {
			res.Ignored++
			continue
		}
		if err := consume(readFile(dir, device.ID, entry, opts)); err != nil {
			return err
		}
	}
	return nil
}

// fileSnapshot is what must stay the same from the first open of a file to the end of its read.
type fileSnapshot struct {
	volume, indexHigh, indexLow, attrs, tag uint32
	size                                    int64
	modified                                int64
}

func snapshot(f *os.File) (fileSnapshot, error) {
	var info windows.ByHandleFileInformation
	if err := winInfo(handleOf(f), &info); err != nil {
		return fileSnapshot{}, err
	}
	attrs, tag, err := winAttrTag(f)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{
		volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, attrs: attrs, tag: tag,
		size:     int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow),
		modified: winTime(int64(info.LastWriteTime.HighDateTime)<<32 | int64(info.LastWriteTime.LowDateTime)).UnixNano(),
	}, nil
}

// cloudErr is true for an error from the cloud files filter, such as a refused download.
func cloudErr(err error) bool {
	var status windows.NTStatus
	if errors.As(err, &status) {
		return winCloudStatus(uint32(status))
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && winCloudErrno(uint32(errno))
}

func readFile(dir *os.File, device string, e winEntry, opts Options) File {
	out := File{Device: device, Name: e.Name, Size: e.Size, Modified: e.Modified}
	fail := func(state, reason string) File {
		out.State, out.Reason, out.Data = state, reason, nil
		return out
	}
	if state, reason := winFileState(e, opts.MaxBytes); state != "" {
		return fail(state, reason)
	}
	if known, ok := opts.Known[Key{device, e.Name}]; ok && known.Size == e.Size && known.ModNanos == e.Modified.UnixNano() {
		out.State = StateUnchanged
		return out
	}
	opened := func(access uint32) (*os.File, fileSnapshot, string) {
		f, err := openChild(handleOf(dir), e.Name, false, access)
		if err != nil {
			if cloudErr(err) {
				return nil, fileSnapshot{}, StateCloudOnly
			}
			return nil, fileSnapshot{}, StateFailed
		}
		snap, err := snapshot(f)
		if err != nil {
			_ = f.Close()
			return nil, fileSnapshot{}, StateFailed
		}
		return f, snap, ""
	}
	failOpen := func(state string) File {
		if state == StateCloudOnly {
			return fail(StateCloudOnly, "scout_file_cloud_only")
		}
		return fail(StateFailed, "scout_file_unreadable")
	}
	// First open for attributes only: nothing a download filter acts on.
	meta, before, state := opened(windows.FILE_READ_ATTRIBUTES)
	if state != "" {
		return failOpen(state)
	}
	_ = meta.Close()
	current := winEntry{Name: e.Name, Attributes: before.attrs, Tag: before.tag, Size: before.size}
	if state, reason := winFileState(current, opts.MaxBytes); state != "" {
		return fail(state, reason)
	}
	if before.size != e.Size || before.modified != e.Modified.UnixNano() {
		return fail(StateFailed, "scout_file_changed_during_read")
	}
	file, bound, state := opened(windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES)
	if state != "" {
		return failOpen(state)
	}
	defer func() { _ = file.Close() }()
	if bound != before {
		return fail(StateFailed, "scout_file_changed_during_read")
	}
	data, err := winReadAll(io.LimitReader(file, opts.MaxBytes+1))
	if err != nil {
		if cloudErr(err) {
			return fail(StateCloudOnly, "scout_file_cloud_only")
		}
		return fail(StateFailed, "scout_file_unreadable")
	}
	if int64(len(data)) > opts.MaxBytes {
		return fail(StateTooLarge, "scout_file_too_large")
	}
	after, err := snapshot(file)
	if err != nil {
		return fail(StateFailed, "scout_file_unreadable")
	}
	if after != before || int64(len(data)) != after.size {
		return fail(StateFailed, "scout_file_changed_during_read")
	}
	out.State, out.Data = StateRead, data
	return out
}

// Discover returns the app folders under the user's OneDrive folders, named by the OneDrive,
// OneDriveCommercial and OneDriveConsumer environment variables OneDrive sets, that hold a Scout
// session layout (sessions\devices), sorted. It looks only one level below each Apps folder and
// reads no file. home is not used on Windows.
func Discover(ctx context.Context, _ string) ([]string, error) {
	found := []string{}
	err := withGuard(ctx, func() error {
		seen := map[string]bool{}
		for _, name := range []string{"OneDriveCommercial", "OneDriveConsumer", "OneDrive"} {
			if err := ctx.Err(); err != nil {
				return err
			}
			root := winGetenv(name)
			if root == "" || seen[strings.ToLower(root)] {
				continue
			}
			seen[strings.ToLower(root)] = true
			found = append(found, appFolders(root)...)
		}
		return nil
	})
	slices.Sort(found)
	return found, err
}

// appFolders lists root\Apps and keeps the folders with a sessions\devices folder. A folder that
// cannot be listed contributes nothing.
func appFolders(root string) []string {
	appsPath := filepath.Join(root, "Apps")
	apps, err := openPath(appsPath)
	if err != nil {
		return nil
	}
	defer func() { _ = apps.Close() }()
	entries, err := winList(apps)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if !winDirReadable(e.Attributes, e.Tag) || hasLayout(apps, e.Name) != nil {
			continue
		}
		out = append(out, filepath.Join(appsPath, e.Name))
	}
	return out
}

func hasLayout(apps *os.File, name string) error {
	current := apps
	for _, part := range []string{name, "sessions", "devices"} {
		next, err := openFolder(handleOf(current), part)
		if current != apps {
			_ = current.Close()
		}
		if err != nil {
			return err
		}
		current = next
	}
	return current.Close()
}
