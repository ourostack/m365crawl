package store

import (
	"context"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"testing"
	"time"
)

func TestEngageWriteFaultSweeps(t *testing.T) {
	for _, state := range []string{"begin", "first", "reuse", "metadata", "failure"} {
		t.Run(state, func(t *testing.T) {
			var a KnowledgeAttempt
			sweepFaults(t, func(t *testing.T, s *Store) {
				if state == "reuse" || state == "metadata" {
					commitEngage(t, s, engageResult("A"), base)
				}
				if state != "begin" {
					a = beginEngage(t, s, base.Add(time.Hour))
				}
			}, func(ctx context.Context, s *Store) error {
				if state == "begin" {
					_, err := s.BeginEngageAttempt(ctx, KnowledgeInput{Kind: EngageKind, Platform: "browser", Locator: EngageHomeURL, At: base})
					return err
				}
				if state == "failure" {
					return s.RecordEngageFailure(ctx, a, EngageFailure{"failed", "engage_capture_failed", base})
				}
				r := engageResult("A")
				if state == "metadata" {
					r.State = "metadata_only"
					r.Threads = nil
					r.Losses = []engagecontent.Loss{{Code: "native_content_not_observed", Count: 1}}
				}
				return s.CommitEngageCapture(ctx, a, EngageCapture{r, base, base})
			})
		})
	}
}
func TestEngageReadFaultSweeps(t *testing.T) {
	setup := func(t *testing.T, s *Store) { commitEngage(t, s, engageResult("A"), base) }
	for _, name := range []string{"sources", "snapshot", "detail", "receipt"} {
		t.Run(name, func(t *testing.T) {
			op := func(ctx context.Context, s *Store) error {
				switch name {
				case "sources":
					_, err := s.EngageSources(ctx)
					return err
				case "snapshot":
					_, err := s.EngageSnapshot(ctx, EngageFilter{Query: "syntheticword", Limit: 1})
					return err
				case "receipt":
					_, _, err := s.EngageAttemptReceipt(ctx, KnowledgeAttempt{ID: 1, LocatorID: 1})
					return err
				default:
					_, err := s.EngageDetail(ctx, 1)
					return err
				}
			}
			sweepFaults(t, setup, op)
			sweepReadFaults(t, setup, op)
		})
	}
}
