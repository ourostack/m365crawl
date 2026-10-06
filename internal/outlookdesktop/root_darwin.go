//go:build darwin

package outlookdesktop

import "path/filepath"

// platformRoot is where the new Outlook for Mac keeps its profiles, inside the Office group
// container.
func platformRoot(home string) (string, error) {
	return filepath.Join(home, "Library", "Group Containers", "UBF8T346G9.Office", "Outlook", "Outlook 15 Profiles"), nil
}
