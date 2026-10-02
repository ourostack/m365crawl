//go:build !windows

package store

type unsafeArchivePathError struct{}

func (*unsafeArchivePathError) Error() string { return "unsafe archive path" }

func prepareArchiveForWrite(path string) error { return ensureParent(path) }

func finalizeArchiveFile(path string) error { return chmodFile(path, 0o600) }
