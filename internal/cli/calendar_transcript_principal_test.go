package cli

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestCalendarOutlookTranscriptPrincipalIsolation(t *testing.T) {
	e := trEnv(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	outlook := trEvent()
	outlook.Source, outlook.AccountID = calendar.SourceOutlook, "outlook/Main"
	window := calendar.Window{Source: calendar.SourceOutlook, AccountID: outlook.AccountID,
		Start: outlook.Start.Add(-24 * time.Hour), End: outlook.End.Add(24 * time.Hour),
		SyncedAt: trFetchedAt, CacheFreshAt: trFetchedAt}
	if _, err := calendar.ApplySnapshot(ctx, db, window, []calendar.Event{outlook}, trFetchedAt); err != nil {
		t.Fatal(err)
	}
	id := store.EventID(outlook.AccountID, calendar.Key(outlook))
	unlinked := trJSON(t, e, "calendar", "event", id)
	if unlinked["account_id"] != outlook.AccountID {
		t.Fatalf("unlinked Outlook event changed principal: %v", unlinked["account_id"])
	}
	for _, notice := range noticesOf(unlinked) {
		if strings.Contains(notice, "transcript parts are not fetched") {
			t.Fatalf("unlinked Outlook principal borrowed a Teams account's transcripts: %s", notice)
		}
	}
	if parts, _ := unlinked["transcripts"].([]any); len(parts) != 0 {
		t.Fatalf("unlinked Outlook event borrowed foreign transcript parts: %v", parts)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := calendar.LinkAccount(ctx, tx, calendar.SourceOutlook, outlook.AccountID, trAccount, "manual", trFetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, account := range [][]string{nil, {"--account", trAccount}} {
		args := append(append([]string{}, account...), "calendar", "event", id)
		linked := trJSON(t, e, args...)
		if linked["account_id"] != trAccount || len(eventTranscripts(t, linked)) != 1 {
			t.Fatalf("legitimate link did not select the real Teams transcript principal: %v", linked)
		}
		command := ""
		for _, notice := range noticesOf(linked) {
			if tail, ok := strings.CutPrefix(notice, "1 of 3 transcript parts are not fetched; run "); ok {
				command = tail
			}
		}
		got := attendeeCommandWords(t, command)
		eventID, ok := linked["event_id"].(string)
		if !ok {
			t.Fatal("linked event lost its stable identity")
		}
		want := []string{"m365crawl", "--db", e.db, "--format", "json",
			"--account", trAccount, "transcripts", "fetch", eventID}
		if !slices.Equal(got, want) {
			t.Fatalf("transcript remedy lost the real principal/archive/event context:\ngot %q\nwant %q", got, want)
		}
	}
}

func TestTranscriptFetchRecoveryCommandKeepsDestinationAccountScope(t *testing.T) {
	const projected = "00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-0000000000a1"
	explicit := &teamsdesktop.Account{
		TenantID: "00000000-0000-4000-8000-000000000002",
		UserID:   "00000000-0000-4000-8000-0000000000a2",
	}
	for _, format := range []struct {
		value output.Format
		name  string
	}{{output.JSON, "json"}, {output.Text, "text"}} {
		for _, tc := range []struct {
			name, account string
			explicit      *teamsdesktop.Account
			wantAccount   []string
		}{
			{"outlook-is-not-a-teams-filter", "outlook/Main", nil, nil},
			{"explicit-teams-filter-wins", "outlook/Main", explicit,
				[]string{"--account", "00000000-0000-4000-8000-000000000002/00000000-0000-4000-8000-0000000000a2"}},
			{"projected-teams-filter-retained", projected, nil, []string{"--account", projected}},
		} {
			t.Run(format.name+"/"+tc.name, func(t *testing.T) {
				e := newEnv(t)
				rt := &runtime{dbPath: e.db, root: e.root, outlookRoot: e.root, outlookOn: true,
					format: format.value, account: tc.explicit, fields: []string{"subject"}}
				eventID := trEventID()
				got := attendeeCommandWords(t, rt.transcriptFetchRecoveryCommand(tc.account, eventID))
				want := []string{"m365crawl", "--db", e.db, "--format", format.name}
				want = append(want, tc.wantAccount...)
				want = append(want, "transcripts", "fetch", eventID)
				if !slices.Equal(got, want) {
					t.Fatalf("transcript recovery changed destination-valid literal context:\ngot %q\nwant %q", got, want)
				}
			})
		}
	}
}
