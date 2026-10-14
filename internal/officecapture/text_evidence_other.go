//go:build !darwin && !windows

package officecapture

import (
	"errors"
	"os"
)

func heldTextEvidence(*os.File) (FileEvidence, error) {
	return FileEvidence{}, errors.New("resident acquisition unsupported")
}
