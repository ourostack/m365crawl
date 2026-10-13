//go:build !darwin && !linux && !windows

package sourcecapture

import (
	"errors"
	"os"
)

// LockSQLite refuses on platforms without a qualified SQLite locking protocol.
func LockSQLite(*os.File) (func() error, error) {
	return nil, errors.New("unsupported SQLite locking platform")
}
