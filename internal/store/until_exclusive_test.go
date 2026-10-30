package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Every date-bounded read filters on the half-open window [Since, Until): an item stamped exactly
// at Until belongs to the next window, so adjacent windows never count it twice (#257).
func TestUntilIsExclusiveInEveryFilter(t *testing.T) {
	ctx := context.Background()
	t.Run("messages", func(t *testing.T) {
		s := newStore(t)
		must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c", "m1", "hello", base)}))
		if rows, _ := must2(s.Messages(ctx, Filter{Until: base})); len(rows) != 0 {
			t.Fatalf("a message at Until is outside the window: %v", ids(rows))
		}
		if rows, _ := must2(s.Messages(ctx, Filter{Since: base, Until: base.Add(time.Millisecond)})); len(rows) != 1 {
			t.Fatalf("a message at Since is inside the window: %v", ids(rows))
		}
	})
	t.Run("cloud", func(t *testing.T) {
		s := newStore(t)
		r := cloudRequest(t, "page", "")
		result := cloudResult("page", "")
		modified := "2026-01-01T00:00:00Z"
		at, _ := time.Parse(time.RFC3339Nano, modified)
		result.File.ModifiedRaw, result.File.ModifiedAt = &modified, &at
		commitCloud(t, s, beginCloud(t, s, r, base), r, result, base)
		if snap, err := s.CloudSnapshot(ctx, CloudFilter{ModifiedUntil: at}); err != nil || len(snap.Rows) != 0 {
			t.Fatalf("an observation at ModifiedUntil is outside the window: %d %v", len(snap.Rows), err)
		}
		if snap, err := s.CloudSnapshot(ctx, CloudFilter{ModifiedSince: at, ModifiedUntil: at.Add(time.Second)}); err != nil || len(snap.Rows) != 1 {
			t.Fatalf("an observation at ModifiedSince is inside the window: %d %v", len(snap.Rows), err)
		}
	})
	t.Run("local text", func(t *testing.T) {
		s := newStore(t)
		c := localCapture(t, `<w:p><w:r><w:t>boundaryword</w:t></w:r></w:p>`, "A")
		commitLocal(t, s, c, base)
		at := *c.Evidence.ModifiedAt
		if snap, err := s.LocalTextSnapshot(ctx, LocalTextFilter{ModifiedUntil: at}); err != nil || len(snap.Rows) != 0 {
			t.Fatalf("a capture at ModifiedUntil is outside the window: %d %v", len(snap.Rows), err)
		}
		if snap, err := s.LocalTextSnapshot(ctx, LocalTextFilter{ModifiedSince: at, ModifiedUntil: at.Add(time.Second)}); err != nil || len(snap.Rows) != 1 {
			t.Fatalf("a capture at ModifiedSince is inside the window: %d %v", len(snap.Rows), err)
		}
	})
	t.Run("engage", func(t *testing.T) {
		s := newStore(t)
		r := engageResult("A")
		commitEngage(t, s, r, base)
		at := *r.Threads[0].StarterUpdatedAt
		after := at.Add(time.Second)
		if snap, err := s.EngageSnapshot(ctx, EngageFilter{ModifiedUntil: &at}); err != nil || len(snap.Rows) != 0 {
			t.Fatalf("an observation at ModifiedUntil is outside the window: %d %v", len(snap.Rows), err)
		}
		if snap, err := s.EngageSnapshot(ctx, EngageFilter{ModifiedSince: &at, ModifiedUntil: &after}); err != nil || len(snap.Rows) != 1 {
			t.Fatalf("an observation at ModifiedSince is inside the window: %d %v", len(snap.Rows), err)
		}
	})
}
