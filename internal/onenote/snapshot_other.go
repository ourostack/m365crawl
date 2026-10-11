//go:build !darwin && !linux && !windows

package onenote

import (
	"errors"
	"os"
)

func lockIndex(*os.File) (func() error, error) {
	return nil, errors.New("unsupported SQLite locking platform")
}

func snapshotSingleLink(*os.File) error {
	return errors.New("unsupported SQLite file identity")
}

func snapshotIdentity(os.FileInfo) string { return "" }
