//go:build windows

package cli

import (
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"
)

// This exercises the actual Windows adapter, not POSIX argv tokenization or a shell.
func TestRecoveryWindowsPrefixGlobalsRefuseExpansion(t *testing.T) {
	base := runtime{
		dbPath: `C:\archives\family folder\selected.db`, format: output.JSON,
		root: `C:\teams\cache`, outlookRoot: `C:\outlook\cache`, outlookOn: true,
	}
	const safe = `m365crawl --db "C:\archives\family folder\selected.db" --no-sync --format json --account outlook/Main --teams-root C:\teams\cache --outlook-root C:\outlook\cache sync`
	if got := base.recoveryCommand(true, "outlook/Main", "sync"); got != safe {
		t.Fatalf("qualification: ordinary Windows globals lost literal context: %q", got)
	}
	for _, value := range []string{"$family", "%USERNAME%", "`whoami`", "$(Get-Item .)"} {
		for _, field := range []string{"archive", "teams", "outlook", "account"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				rt, account := base, "outlook/Main"
				switch field {
				case "archive":
					rt.dbPath = `C:\archives\` + value + `\selected.db`
				case "teams":
					rt.root = `C:\teams\` + value
				case "outlook":
					rt.outlookRoot = `C:\outlook\` + value
				case "account":
					account = "outlook/" + value
				}
				command := rt.recoveryCommand(true, account, "sync")
				if strings.Contains(command, "m365crawl ") || strings.Contains(command, value) {
					t.Errorf("windows_prefix_expansion_emitted field=%s command=%q", field, command)
				}
				if strings.TrimSpace(command) == "" {
					t.Error("unsafe globals need explicit non-executable guidance")
				}
			})
		}
	}
}
