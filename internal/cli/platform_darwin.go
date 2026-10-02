//go:build darwin

package cli

import (
	"os"
	"path/filepath"

	"github.com/ourostack/teamscrawl/internal/errs"
)

func defaultArchivePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errs.Usage("cannot find the home directory; pass --db")
	}
	return filepath.Join(home, ".teamscrawl", "teamscrawl.db"), nil
}

func fullDiskAccessDoctorCheck(code string, coded *errs.Coded) check {
	switch code {
	case errs.CodeTeamsNotInstalled:
		return check{Name: "full_disk_access", Detail: "not checked: Teams is not installed", Fix: "Fix teams_installed first."}
	case errs.CodeNoFullDiskAccess:
		return check{Name: "full_disk_access", Detail: coded.Message, Fix: fdaFix()}
	default:
		return check{Name: "full_disk_access", OK: true, Detail: "the Teams container is readable"}
	}
}

func teamsOriginPermissionCheck(string, *errs.Coded) (check, bool) {
	return check{}, false
}

func outputFix(c *errs.Coded) string {
	if c.Code == errs.CodeNoFullDiskAccess {
		return fdaFix()
	}
	return c.Fix
}
