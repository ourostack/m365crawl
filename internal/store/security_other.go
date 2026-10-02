//go:build !windows

package store

import "os"

type unsafeArchivePathError struct{}

func (*unsafeArchivePathError) Error() string { return "unsafe archive path" }

func prepareArchiveForWrite(path string) error { return ensureParent(path) }

func prepareArchiveDir(path string) error { return ensureParent(path) }

func finalizeArchiveFile(path string) error { return chmodFile(path, 0o600) }

func secureLockHandle(_ *os.File) error { return nil }
