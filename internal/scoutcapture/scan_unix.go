//go:build darwin || linux

package scoutcapture

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ourostack/m365crawl/internal/scoutsessions"
)

// Seams for tests; production uses the system calls.
var (
	evalSymlinks = filepath.EvalSymlinks
	openat       = unix.Openat
	fstatat      = unix.Fstatat
	fstat        = unix.Fstat
	readNames    = func(dir *os.File) ([]string, error) { return dir.Readdirnames(-1) }
	readAll      = io.ReadAll
)

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// Supported is true: acquisition is qualified on this platform.
func Supported() bool { return true }

// Scan lists every device folder of the app folder at opts.Root and hands each session file to
// consume, in device then file name order. It returns an *Error when the folder cannot be scanned
// at all, consume's error, or the context's error; a device folder or file that cannot be read is
// reported in the result and the scan goes on.
func Scan(ctx context.Context, opts Options, consume func(File) error) (Result, error) {
	if !filepath.IsAbs(opts.Root) || filepath.Clean(opts.Root) != opts.Root {
		return Result{}, &Error{Code: "scout_root_invalid"}
	}
	root, err := evalSymlinks(opts.Root)
	if err != nil {
		return Result{}, &Error{Code: "scout_root_invalid"}
	}
	res := Result{Root: root}
	err = withGuard(ctx, func() error { return scan(ctx, opts, consume, &res) })
	return res, err
}

func openDir(parent int, name string) (*os.File, error) {
	fd, err := openat(parent, name, dirFlags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil //nolint:gosec // G115: a descriptor the kernel returned
}

func scan(ctx context.Context, opts Options, consume func(File) error, res *Result) error {
	root, err := openDir(unix.AT_FDCWD, res.Root)
	if err != nil {
		return &Error{Code: "scout_root_invalid"}
	}
	defer func() { _ = root.Close() }()
	if !qualifiedFS(int(root.Fd())) { //nolint:gosec // G115: a descriptor
		return &Error{Code: "scout_root_unqualified"}
	}
	sessions, err := openDir(int(root.Fd()), "sessions") //nolint:gosec // G115: a descriptor
	if err != nil {
		return &Error{Code: "scout_root_invalid"}
	}
	defer func() { _ = sessions.Close() }()
	devices, err := openDir(int(sessions.Fd()), "devices") //nolint:gosec // G115: a descriptor
	if err != nil {
		return &Error{Code: "scout_root_invalid"}
	}
	defer func() { _ = devices.Close() }()
	names, err := readNames(devices)
	if err != nil {
		return &Error{Code: "scout_devices_unreadable"}
	}
	slices.Sort(names)
	for _, name := range names {
		if !scoutsessions.DeviceFolder(name) {
			res.Ignored++
			continue
		}
		device := Device{ID: name}
		err := scanDevice(ctx, int(devices.Fd()), &device, opts, consume, res) //nolint:gosec // G115: a descriptor
		res.Devices = append(res.Devices, device)
		if err != nil {
			return err
		}
	}
	return nil
}

func scanDevice(ctx context.Context, parent int, device *Device, opts Options, consume func(File) error, res *Result) error {
	dir, err := openDir(parent, device.ID)
	if err != nil {
		device.Reason = "scout_device_unreadable"
		return nil
	}
	defer func() { _ = dir.Close() }()
	names, err := readNames(dir)
	if err != nil {
		device.Reason = "scout_device_unreadable"
		return nil
	}
	device.Listed = true
	slices.Sort(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scoutsessions.SessionFile(name) {
			res.Ignored++
			continue
		}
		if err := consume(readFile(int(dir.Fd()), device.ID, name, opts)); err != nil { //nolint:gosec // G115: a descriptor
			return err
		}
	}
	return nil
}

func modified(st *unix.Stat_t) time.Time {
	return time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)).UTC()
}

func readFile(dir int, device, name string, opts Options) File {
	out := File{Device: device, Name: name}
	fail := func(state, reason string) File {
		out.State, out.Reason, out.Data = state, reason, nil
		return out
	}
	var listed unix.Stat_t
	if err := fstatat(dir, name, &listed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fail(StateFailed, "scout_file_unreadable")
	}
	out.Size, out.Modified = listed.Size, modified(&listed)
	switch {
	case listed.Mode&unix.S_IFMT != unix.S_IFREG:
		return fail(StateUnsafe, "scout_file_unsafe")
	case dataless(&listed):
		return fail(StateCloudOnly, "scout_file_cloud_only")
	case listed.Size > opts.MaxBytes:
		return fail(StateTooLarge, "scout_file_too_large")
	}
	if known, ok := opts.Known[Key{device, name}]; ok && known.Size == listed.Size && known.ModNanos == out.Modified.UnixNano() {
		out.State = StateUnchanged
		return out
	}
	fd, err := openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if blocked(err) {
			return fail(StateCloudOnly, "scout_file_cloud_only")
		}
		return fail(StateFailed, "scout_file_unreadable")
	}
	file := os.NewFile(uintptr(fd), name) //nolint:gosec // G115: a descriptor the kernel returned
	defer func() { _ = file.Close() }()
	var before, after unix.Stat_t
	if fstat(fd, &before) != nil {
		return fail(StateFailed, "scout_file_unreadable")
	}
	if before.Dev != listed.Dev || before.Ino != listed.Ino || before.Mode&unix.S_IFMT != unix.S_IFREG {
		return fail(StateFailed, "scout_file_changed_during_read")
	}
	if dataless(&before) {
		return fail(StateCloudOnly, "scout_file_cloud_only")
	}
	data, err := readAll(io.LimitReader(file, opts.MaxBytes+1))
	if err != nil {
		if blocked(err) {
			return fail(StateCloudOnly, "scout_file_cloud_only")
		}
		return fail(StateFailed, "scout_file_unreadable")
	}
	if int64(len(data)) > opts.MaxBytes {
		return fail(StateTooLarge, "scout_file_too_large")
	}
	if fstat(fd, &after) != nil {
		return fail(StateFailed, "scout_file_unreadable")
	}
	if after.Size != before.Size || modified(&after) != modified(&before) || int64(len(data)) != after.Size || dataless(&after) {
		return fail(StateFailed, "scout_file_changed_during_read")
	}
	out.State, out.Data, out.Size, out.Modified = StateRead, data, after.Size, modified(&after)
	return out
}

// Discover returns the app folders under the user's local OneDrive folders that hold a Scout
// session layout (sessions/devices), sorted, with links resolved. It looks only one level below
// each OneDrive folder's Apps folder and reads no file.
func Discover(ctx context.Context, home string) ([]string, error) {
	found := []string{}
	err := withGuard(ctx, func() error {
		for _, root := range oneDriveRoots(home) {
			if err := ctx.Err(); err != nil {
				return err
			}
			found = append(found, appFolders(root)...)
		}
		return nil
	})
	slices.Sort(found)
	return found, err
}

// appFolders lists root/Apps and keeps the folders with a sessions/devices directory. A folder
// that cannot be listed contributes nothing.
func appFolders(root string) []string {
	resolved, err := evalSymlinks(filepath.Join(root, "Apps"))
	if err != nil {
		return nil
	}
	apps, err := openDir(unix.AT_FDCWD, resolved)
	if err != nil {
		return nil
	}
	defer func() { _ = apps.Close() }()
	names, err := readNames(apps)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, name := range names {
		var st unix.Stat_t
		if fstatat(int(apps.Fd()), filepath.Join(name, "sessions", "devices"), &st, unix.AT_SYMLINK_NOFOLLOW) == nil && //nolint:gosec // G115: a descriptor
			st.Mode&unix.S_IFMT == unix.S_IFDIR {
			out = append(out, filepath.Join(resolved, name))
		}
	}
	return out
}
