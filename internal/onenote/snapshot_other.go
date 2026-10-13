//go:build !darwin && !linux && !windows

package onenote

import (
	"errors"
	"os"
)

func snapshotSingleLink(*os.File) error {
	return errors.New("unsupported SQLite file identity")
}

func snapshotIdentity(os.FileInfo) string { return "" }
