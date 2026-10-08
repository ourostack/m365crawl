package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/browser/browsertest"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// testBrowserPath is the browser doctor finds in tests; nothing ever runs it.
const testBrowserPath = "/opt/m365crawl-test/msedge"

// TestMain gives every test a home directory of its own. Outlook is read by default, so a test that
// does not name its roots would otherwise look at the Teams and Outlook data of the machine it runs on.
func TestMain(m *testing.M) {
	browsertest.RunIfFake() // the fake browser of the launch tests is this binary
	// The synthetic SharePoint hosts of the fixtures live under the reserved .invalid domain.
	transcripts.AllowHostSuffixForTests(".sharepoint.example.invalid")
	home, err := os.MkdirTemp("", "m365crawl-cli-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		_ = os.Setenv(k, home)
	}
	// doctor names the browser transcripts fetch would run: the same one on every machine.
	findBrowser = func(string) (string, browser.Kind, error) { return testBrowserPath, browser.KindEdge, nil }
	code := m.Run()
	removeSearchTemplates()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
