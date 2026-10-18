package cli

import (
	"path/filepath"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/store"
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
	return recoveryPlatformCommand(db, flags, args)
}

func (rt *runtime) recoverySyncCommand(st *store.Store) string {
	if rt.mailAccount() != "" {
		return "no executable recovery command: an Outlook read account is not a Teams sync filter; acquire the selected archive and Outlook root separately without --account"
	}
	account := ""
	if rt.account != nil {
		account = rt.account.TenantID + "/" + rt.account.UserID
	}
	args := []string{"sync"}
	link := rt.outlookOn && rt.outlookLink != ""
	if account != "" && link {
		if st == nil || rt.linkPending(st) {
			return "no executable recovery command: cannot combine a Teams account filter with an unconfirmed Outlook link; apply the link separately"
		}
		// An already-applied link needs no new operation, and sync rejects it with --account.
		link = false
	}
	if link {
		args = append(args, "--outlook-account", rt.outlookLink)
	}
	if rt.outlookOn && rt.outlookProfile != "" && account == "" {
		args = append(args, "--outlook-profile", rt.outlookProfile)
	}
	return rt.recoveryCommand(true, account, args...)
}
