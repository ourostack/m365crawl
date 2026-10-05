// Package outlookdesktop finds the new Outlook for Mac profile on this machine, takes a
// consistent private copy of its HxStore.hxd container and fingerprints the live file. Nothing
// here writes to Outlook's storage: the original is only ever opened O_RDONLY, once per copy
// attempt, and no file contents are logged or returned in an error. The package depends on the
// standard library and internal/errs only.
package outlookdesktop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/ourostack/teamscrawl/internal/errs"
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

// Discover lists the profiles under root (the Outlook 15 Profiles directory; the caller passes
// it, tests pass a temporary directory). A profile is a subdirectory that holds a regular
// HxStore.hxd. classicOnly names subdirectories that have only the classic Data/Outlook.sqlite
// (the old Outlook; it is not read). Profiles and classicOnly are sorted by name. Having no
// profile is not an error: both lists are empty. Errors: *RootNotFoundError when root is
// missing, and *errs.Coded no_full_disk_access when macOS denies access.
func Discover(root string) (profiles []Profile, classicOnly []string, err error) {
	ents, err := readDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, &RootNotFoundError{Root: root}
		}
		return nil, nil, mapFSError(root, err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		hasStore, err := isRegular(filepath.Join(dir, StoreFileName))
		if err != nil {
			return nil, nil, err
		}
		if hasStore {
			profiles = append(profiles, Profile{Name: e.Name(), Dir: dir, StorePath: filepath.Join(dir, StoreFileName)})
			continue
		}
		hasClassic, err := isRegular(filepath.Join(dir, filepath.FromSlash(classicSQLiteRel)))
		if err != nil {
			return nil, nil, err
		}
		if hasClassic {
			classicOnly = append(classicOnly, e.Name())
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	sort.Strings(classicOnly)
	return profiles, classicOnly, nil
}

// isRegular reports whether path is a regular file. A missing path is false, not an error.
func isRegular(path string) (bool, error) {
	info, err := statPath(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, mapFSError(path, err)
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
