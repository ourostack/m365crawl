package syncer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeOrdinaryBootstrap(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	o.OutlookMinReadInterval = 0
	now := time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC)
	previous := outlookNow
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = previous })
	run(t, o)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.OutlookReadReceipt(ctx, "outlook/Main")
	if err != nil || receipt.Origin != filepath.Join(root, "Main", "HxStore.hxd") {
		t.Fatalf("ordinary decode has no actual origin: %+v %v", receipt, err)
	}
	prior, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.CommitOutlook(ctx, store.OutlookBatch{Account: "outlook/Main", At: now, Zone: time.UTC, Stamp: store.OutlookStamp(outlookMapperVersion, time.UTC)},
		func(store.CalendarResult) store.Run {
			return store.Run{StartedAt: now, FinishedAt: now, Source: "outlook|Main", Fingerprint: prior.Fingerprint, Status: "ok"}
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	rep, _ := run(t, o)
	if sourceKeyed(t, rep, "outlook|Main").Status != StatusSkippedInterval {
		t.Fatal("origin bootstrap bypassed minimum interval")
	}
	now = now.Add(OutlookMinReadInterval)
	rep, _ = run(t, o)
	if sourceKeyed(t, rep, "outlook|Main").Status != StatusOK {
		t.Fatal("eligible unchanged fingerprint did not bootstrap origin")
	}
	s, err = store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	receipt, err = s.OutlookReadReceipt(ctx, "outlook/Main")
	if err != nil || receipt.ID <= result.OutlookReadID || receipt.Origin != filepath.Join(root, "Main", "HxStore.hxd") {
		t.Fatal("bootstrap did not establish a new qualified actual decode")
	}
}
