package protocol

import (
	"errors"
	"os"
	"path/filepath"
)

// StorageOutsideSource checks complete native directory identities at component boundaries, so case/short-name/volume aliases cannot hide ancestry. A shared ancestor alone is not overlap.
func StorageOutsideSource(source string, writable ...string) error {
	sourceID, err := NativeIdentity(source, true)
	if err != nil {
		return refused()
	}
	for _, path := range writable {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return refused()
		}
		current := path
		// A new receipt leaf need not exist yet. Its existing parent still establishes all possible source ancestors before mkdir.
		// #nosec G703 -- Read-only existence check on a clean absolute storage candidate; every existing component is subsequently native/non-reparse validated.
		if _, err := os.Lstat(current); errors.Is(err, os.ErrNotExist) {
			current = filepath.Dir(current)
		} else if err != nil {
			return refused()
		}
		for {
			id, err := NativeIdentity(current, true)
			if err != nil || id == sourceID {
				return refused()
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	return nil
}
