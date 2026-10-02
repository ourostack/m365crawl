//go:build windows

package cli

import (
	"os"
	"path/filepath"

	"github.com/ourostack/teamscrawl/internal/errs"
)

func defaultArchivePath() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errs.Usage("cannot find %LOCALAPPDATA% or the home directory; pass --db")
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "teamscrawl", "teamscrawl.db"), nil
}

func fullDiskAccessDoctorCheck(string, *errs.Coded) check {
	return check{Name: "full_disk_access", OK: true, Detail: "not applicable on Windows"}
}

func teamsOriginPermissionCheck(code string, coded *errs.Coded) (check, bool) {
	if code != errs.CodeNoFullDiskAccess {
		return check{}, false
	}
	return check{Name: "teams_origin", Detail: coded.Message, Fix: coded.Fix}, true
}

func outputFix(c *errs.Coded) string { return c.Fix }
