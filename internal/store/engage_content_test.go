package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func engageResult(user string) engagecontent.Result {
	raw := "2026-10-10T01:00:00Z"
	at, _ := time.Parse(time.RFC3339Nano, raw)
	return engagecontent.Result{State: "observations", Qualification: "native_home_observations",
		Account: engagecontent.SourceAccount{Host: "engage.cloud.microsoft", NetworkID: "native-network", UserID: user},
		Threads: []engagecontent.ThreadObservation{
			{Ordinal: 0, ID: "9007199254740993", NetworkID: "native-network", GroupID: "group", StarterID: "starter", StarterUpdatedRaw: &raw, StarterUpdatedAt: &at, Blocks: []string{"syntheticword", "", "syntheticword"}},
			{Ordinal: 1, ID: "9007199254740993", NetworkID: "native-network", GroupID: "group", StarterID: "starter", Blocks: []string{""}},
		}, Losses: []engagecontent.Loss{{Code: "clock_unmapped", Count: 7}}}
}
func beginEngage(t *testing.T, s *Store, at time.Time) KnowledgeAttempt {
	t.Helper()
	a, err := s.BeginEngageAttempt(context.Background(), KnowledgeInput{Kind: EngageKind, Platform: "browser", Locator: EngageHomeURL, At: at})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func commitEngage(t *testing.T, s *Store, r engagecontent.Result, at time.Time) KnowledgeAttempt {
	t.Helper()
	a := beginEngage(t, s, at)
	if err := s.CommitEngageCapture(context.Background(), a, EngageCapture{Result: r, AcquiredAt: at, AdmittedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	return a
}

// Failure attributed to a guessed/previous account would change these snapshots.
func TestEngageUnknownAttemptAttribution(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := beginEngage(t, s, base)
	if err := s.RecordEngageFailure(ctx, a, EngageFailure{"unavailable", "engage_unavailable", base}); err != nil {
		t.Fatal(err)
	}
	first, err := s.EngageSources(ctx)
	if err != nil || len(first.Registrations) != 1 || len(first.Sources) != 0 || first.Registrations[0].AccountState != "unknown" {
		t.Fatalf("first failure: %+v %v", first, err)
	}
	commitEngage(t, s, engageResult("A"), base.Add(time.Hour))
	commitEngage(t, s, engageResult("B"), base.Add(2*time.Hour))
	before, err := s.EngageSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"failed", "cancelled", "unavailable"} {
		at := base.Add(time.Duration(i+3) * time.Hour)
		a = beginEngage(t, s, at)
		reason := "engage_browser_failed"
		if status == "cancelled" {
			reason = "engage_cancelled"
		}
		if err := s.RecordEngageFailure(ctx, a, EngageFailure{status, reason, at}); err != nil {
			t.Fatal(err)
		}
		after, err := s.EngageSources(ctx)
		if err != nil || !reflect.DeepEqual(before.Sources, after.Sources) || after.Registrations[0].AccountState != "unknown" || *after.Registrations[0].LastSuccessfulInstanceID != before.Sources[1].ID {
			t.Fatalf("account contamination: %+v %v", after, err)
		}
	}
}

func TestEngageSessionOccurrenceHistory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r := engageResult("A")
	commitEngage(t, s, r, base)
	a, err := s.EngageSnapshot(ctx, EngageFilter{})
	if err != nil || len(a.Rows) != 2 {
		t.Fatalf("%+v %v", a, err)
	}
	changed := engageResult("A")
	changed.Threads[0].Blocks = []string{"changed"}
	commitEngage(t, s, changed, base.Add(time.Hour))
	commitEngage(t, s, r, base.Add(2*time.Hour))
	b, err := s.EngageSnapshot(ctx, EngageFilter{})
	if err != nil || len(b.Rows) != 4 {
		t.Fatalf("%+v %v", b, err)
	}
	for _, original := range a.Rows {
		d, err := s.EngageDetail(ctx, original.ID)
		if err != nil || !reflect.DeepEqual(original, d.Observation) {
			t.Fatalf("A-B-A moved original: %+v %v", d, err)
		}
	}
	meta := engageResult("A")
	meta.State = "metadata_only"
	meta.Threads = nil
	meta.Losses = []engagecontent.Loss{{Code: "native_content_not_observed", Count: 1}}
	commitEngage(t, s, meta, base.Add(3*time.Hour))
	d, err := s.EngageDetail(ctx, a.Rows[0].ID)
	if err != nil || !d.Observation.Retained || d.Source.CurrentSession.State != "metadata_only" || d.Source.LastGoodSessionID == nil || !reflect.DeepEqual(d.Observation.SessionLosses, r.Losses) {
		t.Fatalf("retained session loss: %+v %v", d, err)
	}
	only, err := s.EngageSources(ctx)
	encoded, _ := json.Marshal(only)
	if err != nil || len(only.Rows) != 0 || string(encoded) == "" {
		t.Fatal(err)
	}
}

func TestEngageAtomicAdmissionAndStaleFrontiers(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	old := beginEngage(t, s, base)
	commitEngage(t, s, engageResult("A"), base.Add(time.Hour))
	if err := s.CommitEngageCapture(ctx, old, EngageCapture{engageResult("A"), base, base}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatal(err)
	}
	if _, err := s.EngageSources(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`create trigger engage_fault before insert on engage_capture_members begin select raise(abort,'synthetic'); end`); err != nil {
		t.Fatal(err)
	}
	a := beginEngage(t, s, base.Add(2*time.Hour))
	if err := s.CommitEngageCapture(ctx, a, EngageCapture{engageResult("B"), base, base}); err == nil {
		t.Fatal("half publication")
	}
	snap, err := s.EngageSources(ctx)
	if err != nil || len(snap.Sources) != 1 || snap.Registrations[0].AccountState != "unknown" {
		t.Fatal(err)
	}
}

func TestEngageNativeClockColumns(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i, raw := range []string{"9999-12-31T23:30:00-01:00", "9000-01-01T00:00:00Z", "0000-01-01T00:30:00+01:00", "0001-01-01T00:00:00Z"} {
		r := engageResult("A")
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatal(err)
		}

		r.Threads = r.Threads[:1]
		r.Losses[0].Count = 3
		r.Threads[0].StarterUpdatedRaw = &raw
		r.Threads[0].StarterUpdatedAt = &at
		commitEngage(t, s, r, base.Add(time.Duration(i)*time.Hour))
	}
	snap, err := s.EngageSnapshot(ctx, EngageFilter{})
	if err != nil || len(snap.Rows) != 4 || *snap.Rows[0].Thread.StarterUpdatedRaw != "9999-12-31T23:30:00-01:00" {
		t.Fatalf("clock ordering: %+v %v", snap, err)
	}
	since, _ := time.Parse(time.RFC3339Nano, "9000-01-01T00:00:00Z")
	snap, err = s.EngageSnapshot(ctx, EngageFilter{ModifiedSince: &since})
	if err != nil || len(snap.Rows) != 2 {
		t.Fatalf("overflow window: %+v %v", snap, err)
	}
}
