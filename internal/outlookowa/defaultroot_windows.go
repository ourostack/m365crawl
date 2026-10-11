//go:build windows

package outlookowa

import (
	"os"
	"path/filepath"
)

// DefaultRoot is the new Outlook WebView root; it never falls back to another user's home.
func DefaultRoot() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" || !filepath.IsAbs(local) {
		return "", sourceFailure("localappdata_unavailable", nil)
	}
	return filepath.Join(local, "Microsoft", "Olk", "EBWebView"), nil
}
