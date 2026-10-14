package store

import (
	"context"
	"errors"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"testing"
	"time"
)

func TestEngageUnboundAttemptIntegrity(t *testing.T) {
	for _, statement := range []string{
		`update knowledge_attempts set losses_json='{"clock_unmapped":1}'`,
		`update knowledge_attempts set content_digest='borrowed'`,
		`update knowledge_attempts set reason='private-error'`,
		`update knowledge_attempts set coverage='complete'`,
		`update knowledge_attempts set outcome='partial'`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			a := beginEngage(t, s, base)
			if err := s.RecordEngageFailure(context.Background(), a, EngageFailure{"failed", "engage_unavailable", base}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EngageSources(context.Background()); err == nil {
				t.Fatal("unbound corruption hidden")
			}
		})
	}
	s := newStore(t)
	beginEngage(t, s, base)
	if _, err := s.db.Exec(`update knowledge_attempts set coverage='partial'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EngageSources(context.Background()); err == nil {
		t.Fatal("pending corruption hidden")
	}
}

func TestEngageFiltersAndInputs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, in := range []KnowledgeInput{{}, {Kind: EngageKind, Platform: "browser", Locator: EngageHomeURL, AcquisitionSignature: "no", At: base}} {
		if _, err := s.BeginEngageAttempt(ctx, in); err == nil {
			t.Fatal("invalid registration")
		}
	}
	a := beginEngage(t, s, base)
	for _, capture := range []EngageCapture{{}, {Result: engageResult("A"), AcquiredAt: base}, {AcquiredAt: base, AdmittedAt: base}} {
		if err := s.CommitEngageCapture(ctx, a, capture); err == nil {
			t.Fatal("invalid capture")
		}
	}
	for _, failure := range []EngageFailure{{"complete", "engage_unavailable", base}, {"failed", "private", base}} {
		if err := s.RecordEngageFailure(ctx, a, failure); err == nil {
			t.Fatal("invalid failure")
		}
	}
	r := engageResult("A")
	title := "full title"
	r.Threads[0].Title = &title
	if err := s.CommitEngageCapture(ctx, a, EngageCapture{r, base, base}); err != nil {
		t.Fatal(err)
	}
	commitEngage(t, s, engageResult("B"), base.Add(time.Hour))
	since := base.Add(100 * 365 * 24 * time.Hour)
	until := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range []EngageFilter{{SourceID: 1, Limit: 1}, {ModifiedUntil: &until}, {ModifiedSince: &since}, {Query: "absent"}, {SourceID: 999}} {
		snap, err := s.EngageSnapshot(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if f.SourceID == 1 && (len(snap.Rows) != 1 || snap.Rows[0].SourceID != 1 || !snap.Truncated) {
			t.Fatal("source filtering after limit")
		}
	}
	for _, f := range []EngageFilter{{SourceID: -1}, {Limit: -1}} {
		if _, err := s.EngageSnapshot(ctx, f); err == nil {
			t.Fatal("invalid filter")
		}
	}
	for _, id := range []int64{0, 999} {
		if _, err := s.EngageDetail(ctx, id); !errors.Is(err, ErrKnowledgeSourceUnknown) {
			t.Fatal(err)
		}
	}
}

func TestEngageRowOrder(t *testing.T) {
	known := time.Time{}
	a := EngageObservationRow{ID: 2, SourceID: 2, SessionID: 2, Thread: engagecontent.ThreadObservation{ID: "b", Ordinal: 1, StarterUpdatedAt: &known}}
	b := a
	b.ID = 3
	if !engageRowBefore(a, b) || engageRowBefore(b, a) {
		t.Fatal("numeric tie")
	}
	b = a
	b.Thread.Ordinal = 2
	if !engageRowBefore(a, b) {
		t.Fatal("ordinal")
	}
	b = a
	b.SessionID = 3
	if !engageRowBefore(a, b) {
		t.Fatal("session")
	}
	b = a
	b.SourceID = 3
	if !engageRowBefore(a, b) {
		t.Fatal("source")
	}
	b = a
	b.Thread.ID = "c"
	if !engageRowBefore(a, b) {
		t.Fatal("label")
	}
	b = a
	b.Thread.StarterUpdatedAt = nil
	if !engageRowBefore(a, b) {
		t.Fatal("known Go-zero")
	}
}
