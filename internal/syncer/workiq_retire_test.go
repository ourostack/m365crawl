package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Review: sync's profile retirement treated every non-Teams health entry as an Outlook profile, so a
// failed WorkIQ chats outcome was written back as a WorkIQ "no_profile" row and every later sync
// and read failed on it. A WorkIQ entry is never retired as a profile.
func TestWorkIQHealthIsNeverRetiredAsAnOutlookProfile(t *testing.T) {
	isolateTmp(t)
	root, db := outlookRoot(t, "HxStore.hxd"), newDB(t)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	account := teamsdesktop.Account{TenantID: "workiq-tenant", UserID: "workiq-user"}
	at := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if err := s.ApplyAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.BeginWorkIQAttempt(ctx, store.WorkIQScope{Family: "chats", Account: account.TenantID + "/" + account.UserID}, at)
	if err == nil {
		err = s.CompleteWorkIQAttempt(ctx, attempt, store.WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: at})
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		run(t, outlookOpts(db, root))
	}
	if count(t, db, `select count(*) from sync_runs where source like 'workiq:%' and status<>'failed'`) != 0 {
		t.Fatal("sync retired a WorkIQ entry as an Outlook profile")
	}
	read, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	health, err := read.SourceHealth(ctx)
	found := false
	for _, h := range health {
		found = found || h.Source == "workiq" && h.LastErrorCode == "workiq_failed"
	}
	if err != nil || !found {
		t.Fatal("the WorkIQ failure was lost or health failed after an Outlook sync", err, health)
	}
}
