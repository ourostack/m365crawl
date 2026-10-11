package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
)

func TestNotesLiteralSearchDateOrderingAndTruncation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	result := noteFixture("notebook", "Literal OR expression", "alpha literal")
	result.Entities = append(result.Entities,
		onenote.Entity{RowID: 90, Type: 1, GOID: "undated", GUID: "guid-undated", Title: "Undated alpha"},
		onenote.Entity{RowID: 91, Type: 1, GOID: "later", GUID: "guid-later", Title: "Later alpha", ModifiedAt: base.Add(time.Minute)})
	commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", result)
	rows, truncated, err := s.Notes(ctx, NoteFilter{Query: "alpha", Limit: 1})
	if err != nil || !truncated || len(rows) != 1 || rows[0].Title != "Later alpha" {
		t.Fatalf("date ordering/limit: %#v/%t/%v", rows, truncated, err)
	}
	rows, _, err = s.Notes(ctx, NoteFilter{Query: "alpha", Since: base, Until: base.Add(time.Minute)})
	if err != nil || len(rows) != 1 || rows[0].Title != "Literal OR expression" {
		t.Fatal("native date window included unknown or exclusive upper bound")
	}
	rows, _, err = s.Notes(ctx, NoteFilter{Query: "OR"})
	if err != nil || len(rows) != 1 || rows[0].Title != "Literal OR expression" {
		t.Fatal("user query treated literal FTS operator as syntax")
	}
	if _, _, err := s.Notes(ctx, NoteFilter{Query: "*"}); err == nil {
		t.Fatal("empty searchable query accepted")
	}
	if _, err := s.KnowledgeLocators(ctx, 99); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown source: %v", err)
	}
	for _, id := range []int64{-1, 0, 99} {
		if _, err := s.Note(ctx, id); !errors.Is(err, ErrNoteNotFound) {
			t.Fatalf("invalid/missing note %d: %v", id, err)
		}
	}
}

func TestNotesHierarchyMissingAndCycleBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, parent := range []string{"missing", "section"} {
		t.Run(parent, func(t *testing.T) {
			s := newStore(t)
			result := noteFixture("notebook", "Page", "text")
			result.Entities[1].ParentGOID = parent
			commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", result)
			rows, _, err := s.Notes(ctx, NoteFilter{})
			if err != nil {
				t.Fatal(err)
			}
			detail, err := s.Note(ctx, rows[0].ID)
			if err != nil || len(detail.Ancestors) != 1 || detail.GrandparentGOIDs != "notebook" {
				t.Fatalf("bounded native hierarchy: %#v/%v", detail, err)
			}
		})
	}
}

func TestNotesSchemaPartialRepairAndNewerRefusal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`drop table note_elements`); err != nil {
		t.Fatal(err)
	}
	must0(s.Close())
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _, err := ro.Notes(ctx, NoteFilter{}); err != nil || len(rows) != 0 {
		t.Fatal("partial optional schema cannot serve represented notes")
	}
	must0(ro.Close())
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", noteFixture("notebook", "Repaired", "repaired"))
	if _, err := s.db.Exec(`update schema_migrations set version=10`); err != nil {
		t.Fatal(err)
	}
	must0(s.Close())
	if newer, err := Open(ctx, path); err == nil {
		_ = newer.Close()
		t.Fatal("newer schema accepted for writing")
	}
}

func TestKnowledgeRejectsInvalidAdmissionContracts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{}); err == nil {
		t.Fatal("invalid locator input accepted")
	}
	attempt := beginNotes(t, s, "A", base)
	for _, outcome := range []KnowledgeOutcome{
		{Status: "bad", At: base},
		{Status: "complete", At: base},
		{Status: "complete", ContentDigest: "A", Qualification: "unqualified", At: base},
		{Status: "partial", ContentDigest: "A", At: base},
		{Status: "complete", ContentDigest: "A"},
		{Status: "complete", ContentDigest: "A", Boundary: "forged", At: base},
	} {
		if err := s.CommitNotes(ctx, attempt, outcome, noteFixture("notebook", "Page", "text")); err == nil {
			t.Fatalf("invalid notes outcome accepted: %#v", outcome)
		}
	}
	if err := s.RecordKnowledgeFailure(ctx, attempt, KnowledgeOutcome{Status: "complete", At: base}); err == nil {
		t.Fatal("failure API invoked content admission")
	}
	if err := s.RecordKnowledgeFailure(ctx, KnowledgeAttempt{}, KnowledgeOutcome{Status: "failed", At: base}); err == nil {
		t.Fatal("invalid attempt identity accepted")
	}
	commitNotes(t, s, attempt, "complete", "A", noteFixture("notebook", "Page", "text"))
	if err := s.RecordKnowledgeFailure(ctx, attempt, KnowledgeOutcome{Status: "failed", At: base}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatal("already finalized attempt changed terminal state")
	}
}

func TestNotesPreservesNativeSubMillisecondModifiedTime(t *testing.T) {
	s := newStore(t)
	result := noteFixture("notebook", "Page", "text")
	result.Entities[2].ModifiedAt = base.Add(700 * time.Nanosecond)
	commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", result)
	rows, _, err := s.Notes(context.Background(), NoteFilter{})
	if err != nil || len(rows) != 1 || rows[0].ModifiedAt == nil ||
		!rows[0].ModifiedAt.Equal(result.Entities[2].ModifiedAt) {
		t.Fatal("native FILETIME precision lost during archive admission")
	}
}

func TestKnowledgeCorruptLossEvidenceFailsExplicitly(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := beginNotes(t, s, "A", base)
	commitNotes(t, s, attempt, "complete", "A", noteFixture("notebook", "Page", "text"))
	if _, err := s.db.Exec(`update knowledge_attempts set losses_json='not JSON' where id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeSources(ctx, "notes"); err == nil {
		t.Fatal("corrupt losses became healthy coverage")
	}
	next := beginNotes(t, s, "A", base.Add(time.Second))
	if err := s.CommitNotes(ctx, next, KnowledgeOutcome{Status: "unchanged", ContentDigest: "A", At: base}, onenote.Result{}); err == nil {
		t.Fatal("unchanged silently repaired unknown prior coverage")
	}
}

func TestKnowledgeCorruptTimestampsAreNotUnknownSuccess(t *testing.T) {
	for _, field := range []string{"modified_at", "source_observed_at", "started_at"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", noteFixture("notebook", "Page", "text"))
			var statement string
			switch field {
			case "modified_at":
				statement = `update note_entities set modified_at='invalid' where type=1`
			case "source_observed_at":
				statement = `update knowledge_sources set source_observed_at='invalid'`
			default:
				statement = `update knowledge_attempts set started_at='invalid'`
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if field == "modified_at" {
				if _, _, err := s.Notes(ctx, NoteFilter{}); err == nil {
					t.Fatal("corrupt native timestamp concealed as unknown")
				}
			} else if _, err := s.KnowledgeSources(ctx, "notes"); err == nil {
				t.Fatal("corrupt source timestamp concealed as unknown")
			}
		})
	}
}
