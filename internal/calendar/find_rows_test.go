package calendar

import (
	"database/sql"
	"testing"
)

func TestAttendeeResolvedTypedRows(t *testing.T) {
	db := linked(t)
	teams := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.SourceID = "teams-row" })
	outlook := occurrence(t, SourceOutlook, "2026-11-02T09:00:30Z", func(e *Event) { e.SourceID = "native-row" })
	unrelated := outlook
	unrelated.SourceID, unrelated.GlobalID, unrelated.ICalUID = "other-row", "other-uid", "other-uid"
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", teams)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:10:30Z", "2026-11-02T09:10:30Z", outlook, unrelated)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	item, rows, ok, err := FindWithRows(ctx, tx, acctTeams, Key(teams))
	if err != nil || !ok || len(rows) != 2 || item.Principal != acctTeams {
		t.Fatalf("coherent typed group: %d %v %v", len(rows), ok, err)
	}
	found := false
	for _, row := range rows {
		if row.Source == SourceOutlook {
			found = row.AccountID == acctOutlook && row.SourceID == "native-row"
		}
	}
	if !found {
		t.Fatal("merged UID or account-wide expansion replaced actual Outlook tuple")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
