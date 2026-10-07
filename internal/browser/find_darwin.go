//go:build darwin

package browser

import (
	"os"
	"path/filepath"
)

func platformCandidates(k Kind) []string {
	rel := "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
	if k == KindChrome {
		rel = "Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	out := []string{filepath.Join("/Applications", rel)}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, "Applications", rel))
	}
	return out
}
