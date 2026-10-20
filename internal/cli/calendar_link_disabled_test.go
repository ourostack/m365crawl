package cli

import (
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestLinkFixesWhenOutlookDisabled(t *testing.T) {
	rt := &runtime{dbPath: "/synthetic/archive.db", root: "/synthetic/Teams", outlookRoot: "none"}
	got := rt.linkFixes([]string{"outlook/Main"}, []calendar.AccountCoverage{{AccountID: teamsAccount}})
	if len(got) != 1 || !strings.HasPrefix(got[0], "no executable recovery command:") || !strings.Contains(got[0], "--outlook-root") {
		t.Fatalf("disabled Outlook must request an enabled root, not emit an unusable linking command: %v", got)
	}
}
