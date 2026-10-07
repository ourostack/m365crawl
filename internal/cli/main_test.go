package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain gives every test a home directory of its own. Outlook is read by default, so a test that
// does not name its roots would otherwise look at the Teams and Outlook data of the machine it runs on.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "m365crawl-cli-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		_ = os.Setenv(k, home)
	}
	code := m.Run()
	removeSearchTemplates()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
