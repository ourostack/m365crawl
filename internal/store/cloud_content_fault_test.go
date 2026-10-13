package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCloudWriteAtomicFaultSweeps(t *testing.T) {
	r := cloudRequest(t, "stream", "")
	t.Run("begin", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			_, err := s.BeginCloudAttempt(ctx, KnowledgeInput{Kind: CloudStreamKind, Platform: "browser", Locator: r.LocatorKey, At: base}, r)
			return err
		})
	})
	for _, state := range []string{"first", "reuse", "metadata", "failure"} {
		t.Run(state, func(t *testing.T) {
			var attempt KnowledgeAttempt
			sweepFaults(t, func(t *testing.T, s *Store) {
				if state != "first" {
					commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("stream", "T"), base)
				}
				attempt = beginCloud(t, s, r, base.Add(time.Hour))
			}, func(ctx context.Context, s *Store) error {
				result := cloudResult("stream", "T")
				if state == "metadata" {
					result = cloudResult("stream", "")
				}
				if state == "failure" {
					return s.RecordCloudFailure(ctx, attempt, KnowledgeOutcome{Status: "failed", Reason: "cloud_unreadable", At: base.Add(time.Hour)})
				}
				return s.CommitCloudCapture(ctx, attempt, CloudCapture{r, result, base.Add(time.Hour)})
			})
		})
	}
}

func TestCloudReadFaultSweeps(t *testing.T) {
	r := cloudRequest(t, "stream", "T")
	setup := func(t *testing.T, s *Store) {
		commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("stream", "T"), base)
	}
	for _, name := range []string{"request", "sources", "snapshot", "detail"} {
		t.Run(name, func(t *testing.T) {
			op := func(ctx context.Context, s *Store) error {
				switch name {
				case "request":
					_, err := s.CloudRequest(ctx, 1)
					return err
				case "sources":
					_, err := s.CloudSources(ctx, 0)
					return err
				case "snapshot":
					_, err := s.CloudSnapshot(ctx, CloudFilter{Query: "searchableword", Limit: 1})
					return err
				default:
					_, _, err := s.CloudDetail(ctx, 1)
					return err
				}
			}
			sweepFaults(t, setup, op)
			sweepReadFaults(t, setup, op)
		})
	}
}

func TestCloudOptionalAbsenceFiltersAndCorruption(t *testing.T) {
	ctx := context.Background()
	r := cloudRequest(t, "page", "")
	t.Run("old-schema", func(t *testing.T) {
		s := newStore(t)
		for _, table := range []string{"cloud_locator_metadata", "cloud_observations", "cloud_capture_members", "cloud_capture_metadata", "cloud_source_body_state", "cloud_fts"} {
			if _, err := s.db.Exec("drop table " + table); err != nil {
				t.Fatal(err)
			}
		}
		if snapshot, err := s.CloudSnapshot(ctx, CloudFilter{}); err != nil || len(snapshot.Rows) != 0 {
			t.Fatal("optional absence")
		}
		if _, err := s.CloudSources(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
			t.Fatal("optional source")
		}
		if _, err := s.CloudRequest(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
			t.Fatal("optional request")
		}
	})
	t.Run("filters", func(t *testing.T) {
		s := newStore(t)
		a := beginCloud(t, s, r, base)
		result := cloudResult("page", "")
		modified := "2026-01-01T00:00:00Z"
		parsed, _ := time.Parse(time.RFC3339Nano, modified)
		result.File.ModifiedRaw = &modified
		result.File.ModifiedAt = &parsed
		commitCloud(t, s, a, r, result, base)
		for _, filter := range []CloudFilter{{SourceID: 1, ModifiedSince: parsed.Add(-time.Second), ModifiedUntil: parsed.Add(time.Second), Limit: 1}, {ModifiedSince: parsed.Add(time.Second)}} {
			if _, err := s.CloudSnapshot(ctx, filter); err != nil {
				t.Fatal(err)
			}
		}
		title := "Changed"
		result.File.Title = &title
		commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, result, base.Add(time.Hour))
		if snapshot, err := s.CloudSnapshot(ctx, CloudFilter{Limit: 1}); err != nil || !snapshot.Truncated {
			t.Fatal("limit")
		}
		for _, filter := range []CloudFilter{{SourceID: -1}, {Limit: -1}, {SourceID: 999}, {Query: " "}} {
			if _, err := s.CloudSnapshot(ctx, filter); err == nil {
				t.Fatal("invalid filter")
			}
		}
		if _, err := s.CloudSources(ctx, -1); err == nil {
			t.Fatal("negative source")
		}
		if _, err := s.CloudRequest(ctx, 0); err == nil {
			t.Fatal("zero registration")
		}
		for _, id := range []int64{0, 999} {
			if _, _, err := s.CloudDetail(ctx, id); err == nil {
				t.Fatal("invalid detail")
			}
		}
	})
	for _, statement := range []string{
		"update cloud_locator_metadata set request_json='{}'",
		"update cloud_locator_metadata set request_json='not-json'",
		"update cloud_capture_metadata set completed_at='invalid'",
		"update cloud_observations set kind='wrong'",
		"update cloud_observations set representation_digest='wrong'",
		"update cloud_observations set title='wrong'",
		"update cloud_observations set file_modified_at='invented'",
		"update knowledge_sources set boundary='wrong'",
		"update cloud_source_body_state set last_good_observation_id=999",
		"update cloud_observations set result_json='{}'",
		"update cloud_observations set result_json='not-json'",
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			commitCloud(t, s, beginCloud(t, s, r, base), r, cloudResult("page", ""), base)
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CloudSnapshot(ctx, CloudFilter{}); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
	t.Run("input", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.BeginCloudAttempt(ctx, KnowledgeInput{}, r); err == nil {
			t.Fatal("bad registration")
		}
		a := beginCloud(t, s, r, base)
		for _, capture := range []CloudCapture{{Request: r, Result: cloudResult("page", "")}, {Request: r, CompletedAt: base}} {
			if err := s.CommitCloudCapture(ctx, a, capture); err == nil {
				t.Fatal("invalid capture")
			}
		}
		for _, outcome := range []KnowledgeOutcome{{Status: "complete", At: base}, {Status: "failed", Reason: "private", At: base}, {Status: "failed", Reason: "cloud_unreadable", At: base}} {
			if err := s.RecordCloudFailure(ctx, KnowledgeAttempt{LocatorID: 999, ID: 1}, outcome); err == nil {
				t.Fatal("invalid failure")
			}
		}
	})
}
