package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
)

func TestNotesSourceAdmissionFrontierPreservesStaleHistory(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := beginNotes(t, s, "A", base)
	commitNotes(t, s, first, "complete", "A", noteFixture("notebook", "Kept", "kept"))
	next := beginNotes(t, s, "B", base.Add(time.Second))
	if _, err := s.db.Exec(`update knowledge_sources set admitted_attempt=?`, next.ID+1); err != nil {
		t.Fatal(err)
	}
	err := s.CommitNotes(ctx, next, KnowledgeOutcome{Status: "complete", ContentDigest: "B", At: base},
		noteFixture("notebook", "Forbidden", "forbidden"))
	if !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("source frontier not enforced: %v", err)
	}
	if n := rowCount(t, s, `select count(*) from knowledge_attempts where id=? and finished_at is not null`, next.ID); n != 1 {
		t.Fatal("source-frontier stale history was rolled back")
	}
	if n := rowCount(t, s, `select count(*) from note_fts where note_fts match 'forbidden'`); n != 0 {
		t.Fatal("stale source frontier changed searchable content")
	}
}

func TestKnowledgeUnknownSignaturesStayNullAndFailureRetainsNotes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := beginNotes(t, s, "A", base)
	commitNotes(t, s, first, "complete", "A", noteFixture("notebook", "Kept", "retainedword"))
	fail := beginNotes(t, s, "", base.Add(time.Second))
	if err := s.RecordKnowledgeFailure(ctx, fail, KnowledgeOutcome{Status: "failed", Reason: "onenote_snapshot_unavailable", At: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, s, `select count(*) from knowledge_attempts where id=? and acquisition_signature is null and content_digest is null`, fail.ID); n != 1 {
		t.Fatal("unknown acquisition/content evidence was fabricated")
	}
	sources, err := s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 || sources[0].Status != "failed" ||
		sources[0].ContentDigest != "A" || sources[0].AdmittedID != first.ID {
		t.Fatalf("failure erased admitted state: %#v/%v", sources, err)
	}
	notes, _, err := s.Notes(ctx, NoteFilter{Query: "retainedword"})
	if err != nil || len(notes) != 1 || notes[0].CaptureID != first.ID {
		t.Fatal("failed refetch erased last-good notes")
	}
}

func TestKnowledgeAtomicFailureSweeps(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			_, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "notes", Platform: "synthetic", Locator: "/fictional/index.db", At: base})
			return err
		})
	})
	for _, status := range []string{"complete", "partial", "unchanged", "failed"} {
		t.Run(status, func(t *testing.T) {
			var attempt KnowledgeAttempt
			setup := func(t *testing.T, s *Store) {
				first := beginNotes(t, s, "A", base)
				commitNotes(t, s, first, "complete", "A", noteFixture("notebook", "Original", "originalword"))
				attempt = beginNotes(t, s, "B", base.Add(time.Second))
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				outcome := KnowledgeOutcome{Status: status, ContentDigest: "B", At: base.Add(2 * time.Second)}
				if status == "failed" {
					outcome.ContentDigest = ""
					return s.RecordKnowledgeFailure(ctx, attempt, outcome)
				}
				result := noteFixture("notebook", "Replacement", "replacementword")
				switch status {
				case "partial":
					result.Entities = append(result.Entities, onenote.Entity{RowID: 90, Type: 1, GOID: "newpage", GUID: "newguid", Title: "New"})
					result.Elements = append(result.Elements, onenote.Element{RowID: 91, EntityRowID: 90, GOID: "newtext", Text: "newword", Jcid: 393230, Kind: "rich_text"})
					result.Losses = []onenote.Loss{{Code: "onenote_element_unmapped", Count: 1}}
				case "unchanged":
					outcome.ContentDigest = "A"
					result = onenote.Result{}
				}
				return s.CommitNotes(ctx, attempt, outcome, result)
			})
		})
	}
}

func TestKnowledgeReadFailureSweeps(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", noteFixture("notebook", "Original", "originalword"))
	}
	for _, kind := range []string{"sources", "locators", "notes", "detail"} {
		t.Run(kind, func(t *testing.T) {
			sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
				switch kind {
				case "sources":
					_, err := s.KnowledgeSources(ctx, "notes")
					return err
				case "locators":
					_, err := s.KnowledgeLocators(ctx, 0)
					return err
				case "notes":
					_, _, err := s.Notes(ctx, NoteFilter{Query: "originalword", SourceID: 1, Since: base.Add(-time.Second), Until: base.Add(time.Second)})
					return err
				default:
					_, err := s.Note(ctx, 3)
					return err
				}
			})
		})
	}
}
