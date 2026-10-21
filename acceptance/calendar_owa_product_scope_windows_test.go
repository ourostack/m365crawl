//go:build acceptance && windows

package acceptance

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestOWACalendarProductFreshArchiveRejectsForeignCalendarRows(t *testing.T) {
	for _, source := range []string{"outlook", "teams"} {
		t.Run(source, func(t *testing.T) {
			captured := owaObserve(t, owaFixtureProfile("mail", "Synthetic"))
			db := filepath.Join(owaScratch(t), "product-scope.db")
			rep, _, err := syncer.Run(captured.ctx, syncer.Options{
				Root: filepath.Join(captured.root, "no-teams"), DBPath: db, OutlookRoot: captured.root,
				OutlookEnabled: true, OutlookMinReadInterval: -1,
			})
			owaSyncChecks(t, rep, err, captured, 0)
			st := owaArchive(t, db)
			rows, volume, err := owaCalendarProductHistory(captured.ctx, st, captured.account)
			owaCloseArchive(t, st)
			if err != nil || volume != 2 || len(rows) != 2 {
				t.Fatal("fresh_expected_calendar_scope_refused")
			}
			account := "outlook/SyntheticUnexpected"
			if source == "teams" {
				account = "outlook/Synthetic"
			}
			owaCorrupt(t, db, `insert into calendar_source_events(source,account_id,event_key,composite_key,source_id,first_seen_at,seen_at)
				values('`+source+`','`+account+`','synthetic-extra|','synthetic-composite','synthetic-extra','','')`)
			st = owaArchive(t, db)
			rows, volume, err = owaCalendarProductHistory(captured.ctx, st, captured.account)
			owaCloseArchive(t, st)
			if !errors.Is(err, errOWACalendarAccountScope) || len(rows) != 0 || volume != 0 {
				t.Fatal("out_of_scope_calendar_rows_became_qualified_volume")
			}
		})
	}
}
