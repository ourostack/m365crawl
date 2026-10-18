//go:build windows

package cli

import "testing"

func TestRecoveryWindowsLiteralLinkPolicy(t *testing.T) {
	const contextRefusal = "no executable recovery command: selected context requires literal arguments on Windows"
	const linkRefusal = "no executable recovery command: selected link requires literal arguments on Windows"
	for _, tc := range []struct {
		name, db string
		args     []string
		want     string
	}{
		{"safe-concrete", `C:\archives\selected.db`, []string{"sync", "--outlook-account", "tenant/user", "--outlook-profile", "Main"}, ""},
		{"unsafe-context", `C:\archives\%USER%\selected.db`, []string{"sync"}, contextRefusal},
		{"unsafe-link", `C:\archives\selected.db`, []string{"sync", "--outlook-account", "%USER%"}, linkRefusal},
		{"unsafe-profile", `C:\archives\selected.db`, []string{"sync", "--outlook-profile", "$(Get-Item .)"}, linkRefusal},
		{"link-placeholder", `C:\archives\selected.db`, []string{"sync", "--outlook-account", "<tenantId>/<userId>"}, ""},
		{"profile-placeholder-preserves-existing-policy", `C:\archives\selected.db`, []string{"sync", "--outlook-profile", "<tenantId>/<userId>"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = archiveScopedCommand(tc.db, append([]string{"--format", "json"}, tc.args...)...)
			}
			if got := recoveryPlatformCommand(tc.db, []string{"--format", "json"}, tc.args); got != want {
				t.Fatalf("literal policy=%q, want exact %q", got, want)
			}
		})
	}
}
