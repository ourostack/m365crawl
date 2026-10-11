package teamsdesktop

import "os"

// NewPrivateSnapshotRoot reuses the platform-specific snapshot privacy boundary.
func NewPrivateSnapshotRoot() (string, error) { return makeSnapshotRoot() }

// CreatePrivateSnapshotFile creates a new file inside an owned private root.
func CreatePrivateSnapshotFile(path string) (*os.File, error) { return openSnapshotFile(path) }
