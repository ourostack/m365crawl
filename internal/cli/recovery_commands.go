package cli

import (
	"path/filepath"

	"github.com/openclaw/crawlkit/output"
)

// recoveryCommand carries only context accepted by the destination, never item projection flags.
func (rt *runtime) recoveryCommand(acquire bool, account string, args ...string) string {
	db := rt.archiveRef()
	if !filepath.IsAbs(db) {
		return "no executable recovery command: cannot resolve the archive; use an absolute --db"
	}
	format := rt.format
	if format == "" {
		format = output.JSON
	}
	flags := []string{"--format", string(format)}
	if account != "" {
		flags = append(flags, "--account", account)
	}
	if acquire {
		outlook := rt.outlookRoot
		if !rt.outlookOn {
			outlook = "none"
		}
		for _, root := range []struct{ flag, path string }{
			{"--teams-root", rt.root}, {"--outlook-root", outlook},
		} {
			if root.path == "" {
				continue
			}
			if (root.flag != "--outlook-root" || root.path != "none") && !filepath.IsAbs(root.path) {
				wd, err := getwd()
				if err != nil {
					return "no executable recovery command: cannot resolve " + root.flag + "; use an absolute " + root.flag
				}
				root.path = filepath.Join(wd, root.path)
			}
			flags = append(flags, root.flag, root.path)
		}
	}
	return archiveScopedCommand(db, append(flags, args...)...)
}
