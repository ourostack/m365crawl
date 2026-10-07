// Package outlookdesktop finds the new Outlook for Mac profile on this machine, takes a
// consistent private copy of its HxStore.hxd container and fingerprints the live file. Nothing
// here writes to Outlook's storage: the original is only ever opened read-only (O_RDONLY, plus
// O_NONBLOCK where it exists so a swapped-in FIFO cannot block the open), once per copy attempt,
// and no file contents are logged or returned in an error. The package depends on the standard
// library, golang.org/x/sys/unix and internal/errs only.
//
// The copy is best effort. Outlook rewrites HxStore.hxd in place while it runs, and a copy is
// accepted only when the file's size, modification time and identity are the same before the
// copy, right after it and again after a short settle pause. A write that leaves size and
// modification time untouched, for example through a memory mapping that the system has not
// flushed yet, cannot be seen here. One observation on one Mac (Outlook 16.115, 2026-10-05) is that
// Outlook holds HxStore.hxd through ordinary file handles, one read-only and one read-write,
// and memory-maps only HxStore.lock, so the size and time check is meaningful there; the settle
// re-stat is kept anyway. The container reader's per-block checksums are the
// integrity backstop: a torn block fails its checksum and is rejected and counted there.
package outlookdesktop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/ourostack/m365crawl/internal/errs"
)

const (
	// StoreFileName is the container file the new Outlook keeps its mail and calendar in.
	StoreFileName = "HxStore.hxd"

	classicSQLiteRel = "Data/Outlook.sqlite"
)

// ErrNotSupported is matched (errors.Is) by the error DefaultRoot returns on an operating
// system where this package does not know where Outlook keeps its data.
var ErrNotSupported = errors.New("outlookdesktop: not supported on this platform")

// ErrStoreNotFound is matched (errors.Is) by the error Snapshot returns when the store file
// to copy does not exist.
var ErrStoreNotFound = errors.New("outlookdesktop: Outlook store file not found")

// ErrRootNotFound is matched (errors.Is) by the error Discover returns when the profiles
// directory does not exist, which means the new Outlook is not installed for this user.
var ErrRootNotFound = errors.New("outlookdesktop: Outlook profiles directory not found")

// NotSupportedError is the "not supported on this platform" result.
type NotSupportedError struct{ GOOS string }

func (e *NotSupportedError) Error() string {
	return fmt.Sprintf("outlookdesktop: reading the Outlook store is not supported on %s", e.GOOS)
}

// Is makes errors.Is(err, ErrNotSupported) true.
func (e *NotSupportedError) Is(target error) bool { return target == ErrNotSupported }

// RootNotFoundError names the profiles directory that does not exist.
type RootNotFoundError struct{ Root string }

func (e *RootNotFoundError) Error() string {
	return "outlookdesktop: Outlook profiles directory not found at " + e.Root
}

// Is makes errors.Is(err, ErrRootNotFound) true.
func (e *RootNotFoundError) Is(target error) bool { return target == ErrRootNotFound }

// Profile is one Outlook profile directory that holds an HxStore.hxd.
type Profile struct {
	Name      string // directory name, for example "Main Profile"
	Dir       string
	StorePath string // <Dir>/HxStore.hxd
}

// Test seams: directory listing, stat and the platform's profiles directory.
var (
	readDir  = os.ReadDir
	statPath = os.Stat
	userHome = os.UserHomeDir
)

// DefaultRoot is the directory whose subdirectories are Outlook profiles. It returns a
// *NotSupportedError (errors.Is ErrNotSupported) on an operating system other than macOS.
func DefaultRoot() (string, error) {
	home, err := userHome()
	if err != nil {
		return "", errs.Internal(err)
	}
	return platformRoot(home)
}

// SkippedProfile is a profile directory that could not be examined. Discovery carries on
// without it so one unreadable profile does not hide the others.
type SkippedProfile struct {
	Name   string
	Dir    string
	Reason string // "no_full_disk_access" when macOS denied access, else "unreadable"
	Err    error
}

// Discover lists the profiles under root (the Outlook 15 Profiles directory; the caller passes
// it, tests pass a temporary directory). A profile is a subdirectory that holds a regular
// HxStore.hxd. classicOnly names subdirectories that have only the classic Data/Outlook.sqlite
// (the old Outlook; it is not read). skipped names subdirectories whose files could not be
// examined, with the reason. All three lists are sorted by name. Having no profile is not an
// error: the lists are empty.
//
// Errors: *RootNotFoundError when root is missing; *errs.Coded no_full_disk_access when macOS
// denies access to root, or when something was skipped and nothing at all was readable (any
// other failure then is the coded internal error).
func Discover(root string) (profiles []Profile, classicOnly []string, skipped []SkippedProfile, err error) {
	ents, err := readDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil, &RootNotFoundError{Root: root}
		}
		return nil, nil, nil, mapFSError(root, err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		kind, err := classify(dir)
		switch {
		case err != nil:
			reason := "unreadable"
			if errors.Is(err, fs.ErrPermission) {
				reason = "no_full_disk_access"
			}
			skipped = append(skipped, SkippedProfile{Name: e.Name(), Dir: dir, Reason: reason, Err: err})
		case kind == kindProfile:
			profiles = append(profiles, Profile{Name: e.Name(), Dir: dir, StorePath: filepath.Join(dir, StoreFileName)})
		case kind == kindClassic:
			classicOnly = append(classicOnly, e.Name())
		}
	}
	if len(profiles) == 0 && len(classicOnly) == 0 && len(skipped) > 0 {
		return nil, nil, skipped, mapFSError(skipped[0].Dir, skipped[0].Err)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	sort.Strings(classicOnly)
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
	return profiles, classicOnly, skipped, nil
}

type dirKind int

const (
	kindNone dirKind = iota
	kindProfile
	kindClassic
)

// classify says whether dir holds the new store, only the classic SQLite file, or neither.
func classify(dir string) (dirKind, error) {
	hasStore, err := isRegular(filepath.Join(dir, StoreFileName))
	if err != nil {
		return kindNone, err
	}
	if hasStore {
		return kindProfile, nil
	}
	hasClassic, err := isRegular(filepath.Join(dir, filepath.FromSlash(classicSQLiteRel)))
	if err != nil {
		return kindNone, err
	}
	if hasClassic {
		return kindClassic, nil
	}
	return kindNone, nil
}

// isRegular reports whether path is a regular file. A missing path is false, not an error.
func isRegular(path string) (bool, error) {
	info, err := statPath(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// mapFSError turns filesystem errors into coded ones.
func mapFSError(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return errs.NoFullDiskAccess(path, err)
	}
	return errs.Internal(err)
}
