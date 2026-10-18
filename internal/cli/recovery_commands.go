package cli

import (
	"path/filepath"
	goruntime "runtime"
	"strings"

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
	if goruntime.GOOS == "windows" {
		for _, word := range append([]string{db}, flags...) {
			if strings.Trim(word, windowsQuotable) != "" {
				return "no executable recovery command: selected context requires literal arguments on Windows"
			}
		}
		// Link values are concrete acquisition context, not item selectors or help placeholders.
		for i, word := range args {
			if (word == "--outlook-account" || word == "--outlook-profile") && i+1 < len(args) {
				if strings.Trim(args[i+1], windowsQuotable) != "" && args[i+1] != "<tenantId>/<userId>" {
					return "no executable recovery command: selected link requires literal arguments on Windows"
				}
			}
		}
	}
	return archiveScopedCommand(db, append(flags, args...)...)
}

func (rt *runtime) recoverySyncCommand() string {
	account := ""
	if rt.account != nil {
		account = rt.account.TenantID + "/" + rt.account.UserID
	}
	args := []string{"sync"}
	if rt.outlookOn && rt.outlookLink != "" {
		args = append(args, "--outlook-account", rt.outlookLink)
	}
	if rt.outlookOn && rt.outlookProfile != "" {
		args = append(args, "--outlook-profile", rt.outlookProfile)
	}
	return rt.recoveryCommand(true, account, args...)
}
