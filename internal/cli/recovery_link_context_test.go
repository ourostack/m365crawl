package cli

import (
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/output"
)

func TestRecoverySyncCarriesOnlyDestinationValidLinkContext(t *testing.T) {
	a, _ := recoveryArchives(t)
	rt := &runtime{dbPath: a.db, root: a.root, format: output.JSON,
		outlookRoot: filepath.Join(t.TempDir(), "selected Outlook"), outlookOn: true,
		outlookLink: trAccount, outlookProfile: "Selected profile"}
	words := recoveryWords(t, rt.recoverySyncCommand())
	recoveryContext(t, words, a.db, "json")
	for flag, want := range map[string]string{"--outlook-root": rt.outlookRoot,
		"--outlook-account": trAccount, "--outlook-profile": "Selected profile", "--teams-root": a.root} {
		if got := recoveryValue(words, flag); got != want {
			t.Errorf("destination-valid acquisition context %s=%q, want %q", flag, got, want)
		}
	}
	rt.outlookOn = false
	words = recoveryWords(t, rt.recoverySyncCommand())
	if recoveryValue(words, "--outlook-root") != "none" ||
		recoveryValue(words, "--outlook-account") != "" || recoveryValue(words, "--outlook-profile") != "" {
		t.Fatalf("disabled Outlook copied route-incompatible link options: %q", words)
	}
}
