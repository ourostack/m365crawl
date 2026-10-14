package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
)

func TestLocalWriteAtomicFaultSweeps(t *testing.T) {
	c := localCapture(t, `<w:p><w:r><w:t>searchableword</w:t></w:r></w:p>`, "")
	input := officecapture.TextInput{Path: "/synthetic/file.docx", RequestedKind: "docx"}
	t.Run("begin", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			_, err := s.BeginLocalTextAttempt(ctx, KnowledgeInput{Kind: LocalTextKind, Platform: "darwin", Locator: LocalTextLocator(input), At: base}, input)
			return err
		})
	})
	for _, state := range []string{"first", "reuse", "partial", "failure"} {
		t.Run(state, func(t *testing.T) {
			var a KnowledgeAttempt
			sweepFaults(t, func(t *testing.T, s *Store) {
				if state != "first" {
					commitLocal(t, s, c, base)
				}
				a = beginLocal(t, s, "docx", base.Add(time.Second))
			}, func(ctx context.Context, s *Store) error {
				if state == "failure" {
					return s.RecordLocalTextFailure(ctx, a, KnowledgeOutcome{Status: "failed", Reason: "local_text_capture_failed", At: base.Add(time.Second)})
				}
				result := c
				if state == "partial" {
					result.Result.Partial = true
					result.Result.Losses = []documenttext.Loss{{Code: "embedded_content_unmapped", Count: 1}}
				}
				return s.CommitLocalText(ctx, a, LocalTextCommit{Capture: result, RequestOrigin: "explicit"})
			})
		})
	}
}

func TestLocalReadFaultSweeps(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		commitLocal(t, s, localCapture(t, `<w:p><w:r><w:t>searchableword</w:t></w:r></w:p>`, ""), base)
	}
	for _, name := range []string{"request", "sources", "snapshot", "detail"} {
		t.Run(name, func(t *testing.T) {
			op := func(ctx context.Context, s *Store) error {
				switch name {
				case "request":
					_, err := s.LocalTextRequest(ctx, 1)
					return err
				case "sources":
					_, err := s.LocalTextSources(ctx, 0)
					return err
				case "snapshot":
					_, err := s.LocalTextSnapshot(ctx, LocalTextFilter{Query: "searchableword", Limit: 1})
					return err
				default:
					_, err := s.LocalTextDetail(ctx, 1)
					return err
				}
			}
			sweepFaults(t, setup, op)
			sweepReadFaults(t, setup, op)
		})
	}
}

func TestLocalOptionalSchemaFiltersAndHistory(t *testing.T) {
	ctx := context.Background()
	t.Run("absent", func(t *testing.T) {
		s := newStore(t)
		for _, table := range []string{"local_text_locator_metadata", "local_file_snapshots", "local_text_observations", "local_text_capture_members", "local_text_source_state", "local_text_attempt_metadata", "local_text_references", "local_text_fts"} {
			if _, err := s.db.Exec("drop table " + table); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.LocalTextSnapshot(ctx, LocalTextFilter{}); err == nil {
			t.Fatal("current schema missing namespace accepted")
		}
		if _, err := s.db.Exec(`update schema_migrations set version=16`); err != nil {
			t.Fatal(err)
		}
		if snap, err := s.LocalTextSnapshot(ctx, LocalTextFilter{}); err != nil || len(snap.Rows) != 0 {
			t.Fatal("optional absence")
		}
		if _, err := s.LocalTextRequest(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
			t.Fatal("absent request")
		}
		if _, err := s.LocalTextSources(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
			t.Fatal("absent source")
		}
	})
	t.Run("filter", func(t *testing.T) {
		s := newStore(t)
		c := localCapture(t, `<w:p><w:r><w:t>searchableword</w:t></w:r></w:p>`, "A")
		commitLocal(t, s, c, base)
		c = localCapture(t, `<w:p><w:r><w:t>secondword</w:t></w:r></w:p>`, "B")
		commitLocal(t, s, c, base.Add(time.Second))
		at := *c.Evidence.ModifiedAt
		for _, filter := range []LocalTextFilter{
			{SourceID: 1, ModifiedSince: at.Add(-time.Second), ModifiedUntil: at.Add(time.Second), Limit: 1},
			{ModifiedSince: at.Add(time.Second)}, {ModifiedUntil: at.Add(-time.Second)}, {Query: "searchableword", Limit: 1},
		} {
			if _, err := s.LocalTextSnapshot(ctx, filter); err != nil {
				t.Fatal(err)
			}
		}
		if snap, err := s.LocalTextSnapshot(ctx, LocalTextFilter{Limit: 1}); err != nil || !snap.Truncated {
			t.Fatal("limit not reported")
		}
		for _, f := range []LocalTextFilter{{SourceID: -1}, {Limit: -1}, {SourceID: 999}, {Query: " "}} {
			if _, err := s.LocalTextSnapshot(ctx, f); err == nil {
				t.Fatal("invalid filter accepted")
			}
		}
		for _, id := range []int64{0, 999} {
			if _, err := s.LocalTextDetail(ctx, id); err == nil {
				t.Fatal("bad detail")
			}
			if _, err := s.LocalTextRequest(ctx, id); err == nil {
				t.Fatal("bad request")
			}
		}
	})
}
