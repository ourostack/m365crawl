//go:build acceptance

package acceptance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

// TestRealOutlookAutoLink syncs a fresh archive from the real Teams copy and the real Outlook copy with
// no link option, and checks that the profile's own address (the account object, class 0x49 at +5532)
// equals the Teams account's own address (the profiles record, userPrincipalName, mail or email), so the
// two are linked by address. It logs counts only. It skips unless the Outlook root is set.
func TestRealOutlookAutoLink(t *testing.T) {
	root := requireOutlook(t)
	db := filepath.Join(calendarScratch(t), "autolink.db")
	rep, _, err := syncer.Run(context.Background(), syncer.Options{
		Root: calendarTeamsRoot(), DBPath: db,
		OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
	})
	if err != nil {
		t.Fatalf("sync failed with code %s (status %s)", errCode(err), rep.Status)
	}
	st := openArchive(t, db)
	links := rowsOf(t, st, `select method, count(*) from calendar_account_links where source='outlook' and unlinked_at is null group by method`)
	identities := rowsOf(t, st, `select count(*), coalesce(sum(value<>''), 0) from meta where key like 'outlook\_identity:%' escape '\'`)
	t.Logf("outlook identities read: %v; active links by method: %v", identities, links)
	if len(links) != 1 || asString(links[0][0]) != "address" {
		t.Fatalf("the profile was not linked by address: %v", links)
	}
}
