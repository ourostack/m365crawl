//go:build !windows

package store

import "os"

func prepareArchiveForWrite(path string) error { return ensureParent(path) }

func finalizeArchiveFile(path string) error { return chmodFile(path, 0o600) }

func mapArchiveOpenError(err error) error { return err }

// PrivateDir reports whether only its owner can read, write or enter a directory: no permission
// bit for the group or others. It reads the directory's mode only, never its contents.
func PrivateDir(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode().Perm()&0o077 == 0, nil
}
