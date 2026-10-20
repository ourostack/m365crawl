package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestRecoverySyncCarriesOnlyDestinationValidLinkContext(t *testing.T) {
	a, _ := recoveryArchives(t)
	rt := &runtime{dbPath: a.db, root: a.root, format: output.JSON,
		outlookRoot: filepath.Join(t.TempDir(), "selected Outlook"), outlookOn: true,
		outlookLink: trAccount, outlookProfile: "Selected profile"}
	words := recoveryWords(t, rt.recoverySyncCommand(nil))
	recoveryContext(t, words, a.db, "json")
	for flag, want := range map[string]string{"--outlook-root": rt.outlookRoot,
		"--outlook-account": trAccount, "--outlook-profile": "Selected profile", "--teams-root": a.root} {
		if got := recoveryValue(words, flag); got != want {
			t.Errorf("destination-valid acquisition context %s=%q, want %q", flag, got, want)
		}
	}
	rt.outlookOn = false
	words = recoveryWords(t, rt.recoverySyncCommand(nil))
	if recoveryValue(words, "--outlook-root") != "none" ||
		recoveryValue(words, "--outlook-account") != "" || recoveryValue(words, "--outlook-profile") != "" {
		t.Fatalf("disabled Outlook copied route-incompatible link options: %q", words)
	}
}

func TestRecoverySyncRefusesUnconfirmedLinkContext(t *testing.T) {
	for _, state := range []string{"missing", "unapplied", "closed"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			a, b := recoveryArchives(t)
			rt := &runtime{ctx: ctx, dbPath: a.db, root: a.root, format: output.JSON,
				account:     &teamsdesktop.Account{TenantID: tenantA, UserID: userA},
				outlookRoot: filepath.Join(t.TempDir(), "selected Outlook"), outlookOn: true,
				outlookLink: trAccount, outlookProfile: "Selected profile"}
			outlook := rt.outlookRoot
			var st *store.Store
			var before []byte
			if state != "missing" {
				var err error
				st, err = store.Open(ctx, a.db)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				if state == "closed" {
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
				}
				before = recoveryBytes(t, a.db)
			}
			old := runSync
			calls := 0
			runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
				calls++
				return syncer.Report{}, nil, errors.New("unconfirmed-link guidance must not acquire")
			}
			t.Cleanup(func() { runSync = old })
			got := rt.recoverySyncCommand(st)
			if !strings.HasPrefix(got, "no executable recovery command:") || strings.Contains(got, "m365crawl ") {
				t.Fatalf("unconfirmed intent emitted an executable or silently dropped the link: %q", got)
			}
			if calls != 0 || rt.dbPath != a.db || rt.root != a.root || rt.outlookRoot != outlook ||
				!rt.outlookOn || rt.outlookLink != trAccount || rt.outlookProfile != "Selected profile" ||
				rt.account == nil || rt.account.TenantID != tenantA || rt.account.UserID != userA {
				t.Fatalf("guidance acquired or changed selected context: calls=%d runtime=%+v", calls, rt)
			}
			if state == "missing" {
				if _, err := os.Stat(a.db); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("guidance secretly opened the missing selected archive: %v", err)
				}
			} else {
				recoveryUnchanged(t, a.db, before)
			}
			if _, err := os.Stat(b.db); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("guidance opened the unrelated default archive: %v", err)
			}
		})
	}
}
