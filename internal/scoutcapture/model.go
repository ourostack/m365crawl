// Package scoutcapture lists and reads Microsoft Scout session files from a local OneDrive folder
// without ever downloading one. A file OneDrive keeps only in the cloud is reported as cloud_only
// and never opened, and on macOS every open and directory listing runs with dataless-file
// materialization turned off for the thread, so a file that turns cloud-only during the read
// fails instead of being fetched. Acquisition is supported on macOS (APFS) and Linux (local
// filesystems); Windows acquisition is refused until it is verified on a Windows machine.
package scoutcapture

import "time"

// File states, one per session file listed.
const (
	StateRead      = "read"       // resident and read; Data holds its bytes
	StateUnchanged = "unchanged"  // same size and modification time as the archive's last read; not opened
	StateCloudOnly = "cloud_only" // a OneDrive placeholder whose bytes are not on this computer; not opened
	StateUnsafe    = "unsafe"     // not a regular file (a link or something else); not opened
	StateTooLarge  = "too_large"  // larger than the read limit; not read
	StateFailed    = "failed"     // could not be read; Reason says why
)

// Error is a fixed-code failure of a whole scan or discovery. The code is safe to print.
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

// Key names one session file inside an app folder.
type Key struct{ Device, Name string }

// Known is the archive's last read of a file: a file with the same size and modification time is
// reported unchanged without being opened.
type Known struct {
	Size     int64
	ModNanos int64
}

// File is one session file as listed, and read when State is StateRead.
type File struct {
	Device   string
	Name     string
	State    string
	Reason   string // a fixed code for every state except read and unchanged
	Size     int64
	Modified time.Time
	Data     []byte
}

// Device is one per-device folder: Listed is false when its entries could not be listed.
type Device struct {
	ID     string
	Listed bool
	Reason string
}

// Result describes one scan of an app folder. Root is the folder with links resolved.
type Result struct {
	Root    string
	Devices []Device
	// Ignored counts entries that are neither device folders nor session files, such as the
	// app's index files.
	Ignored int
}

// Options select the app folder and the archive's last reads.
type Options struct {
	Root     string
	Known    map[Key]Known
	MaxBytes int64
}

// ErrPlatformUnqualified is returned on a platform where reading OneDrive folders without
// downloading cloud-only files has not been verified.
var ErrPlatformUnqualified = &Error{Code: "scout_platform_unqualified"}
