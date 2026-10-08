// Package browser finds Microsoft Edge (or Chrome), launches it headless with a dedicated
// profile, owns every process it starts, and drives one tab over the Chrome DevTools Protocol
// on loopback. The package never leaves a browser process behind: Close ends the whole process
// group (Unix) or job object (Windows), and a sweep at the next launch ends any orphan.
package browser

import (
	"os"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Kind says which browser a path is.
type Kind string

const (
	KindEdge   Kind = "edge"
	KindChrome Kind = "chrome"
	KindCustom Kind = "custom"
)

// Test seams: where the platform keeps its browsers, and whether a file exists.
var (
	candidates = platformCandidates
	isFile     = func(path string) bool {
		fi, err := os.Stat(path)
		return err == nil && !fi.IsDir()
	}
)

// Find locates a browser. pref is the --browser flag or M365CRAWL_BROWSER: "edge", "chrome",
// or the path of an executable. Empty means Edge, then Chrome.
func Find(pref string) (path string, kind Kind, err error) {
	pref = strings.TrimSpace(pref)
	switch strings.ToLower(pref) {
	case "":
		for _, k := range []Kind{KindEdge, KindChrome} {
			if p, ok := firstExisting(k); ok {
				return p, k, nil
			}
		}
	case string(KindEdge), string(KindChrome):
		k := Kind(strings.ToLower(pref))
		if p, ok := firstExisting(k); ok {
			return p, k, nil
		}
	default:
		if isFile(pref) {
			return pref, KindCustom, nil
		}
	}
	return "", "", errs.NoBrowser()
}

func firstExisting(k Kind) (string, bool) {
	for _, p := range candidates(k) {
		if isFile(p) {
			return p, true
		}
	}
	return "", false
}
