package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
)

func noteFixture(notebook, title, text string) onenote.Result {
	return onenote.Result{
		Entities: []onenote.Entity{
			{RowID: 4, Type: 4, GOID: notebook, GUID: "notebook-guid", Title: "Notebook"},
			{RowID: 2, Type: 2, GOID: "section", GUID: "section-guid", ParentGOID: notebook, Title: "Section"},
			{RowID: 1, Type: 1, GOID: "page", GUID: "page-guid", ParentGOID: "section",
				GrandparentGOIDs: notebook, Title: title, ModifiedAt: base},
		},
		Elements: []onenote.Element{
			{RowID: 10, EntityRowID: 1, GOID: "paragraph", Text: text, Jcid: 393230, Kind: "rich_text"},
			{RowID: 11, EntityRowID: 1, GOID: "ocr", Text: "retained picturewords", Jcid: 393233, Kind: "ocr"},
		},
		Flags: []onenote.Flag{{RowID: 7, ElementRowID: 10, Type: 1, Shape: 2, Status: 3, Label: "Follow up"}},
	}
}

func beginNotes(t *testing.T, s *Store, signature string, at time.Time) KnowledgeAttempt {
	t.Helper()
	attempt, err := s.BeginKnowledgeAttempt(context.Background(), KnowledgeInput{
		Kind: "notes", Platform: "synthetic", Locator: "/fictional/index.db", AcquisitionSignature: signature, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func commitNotes(t *testing.T, s *Store, attempt KnowledgeAttempt, status, digest string, result onenote.Result) {
	t.Helper()
	if err := s.CommitNotes(context.Background(), attempt, KnowledgeOutcome{
		Status: status, ContentDigest: digest, AcquisitionSignature: digest + "-metadata",
		Qualification: "onenote-index-v1/mapper-v1", At: base.Add(time.Minute), SourceObservedAt: base,
	}, result); err != nil {
		t.Fatal(err)
	}
}

func TestNotesSchema9OldReadOnlyAndMigrationPreservesMail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<preserved@example.test>", "preserved mail", 1)))
	var mailRow int64
	if err := s.db.QueryRow(`select rowid from mail_messages where detail_key=21`).Scan(&mailRow); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"note_fts", "note_flags", "note_elements", "note_entities", "knowledge_sources", "knowledge_attempts", "knowledge_locators"} {
		if _, err := s.db.Exec(`drop table ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`update schema_migrations set version=8`); err != nil {
		t.Fatal(err)
	}
	must0(s.Close())
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rows, trunc, err := ro.Notes(ctx, NoteFilter{})
	if err != nil || len(rows) != 0 || trunc {
		t.Fatalf("old optional notes: %#v/%t/%v", rows, trunc, err)
	}
	if need, err := ro.NeedsUpgrade(ctx); err != nil || need {
		t.Fatalf("optional absence contaminated legacy freshness: %t/%v", need, err)
	}
	sources, err := ro.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 0 {
		t.Fatalf("old optional sources: %#v/%v", sources, err)
	}
	must0(ro.Close())
	for range 2 {
		s, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if n := rowCount(t, s, `select max(version) from schema_migrations`); n != 9 {
			t.Fatalf("schema %d, want9", n)
		}
		if n := rowCount(t, s, `select count(*) from mail_fts where rowid=? and mail_fts match 'preserved'`, mailRow); n != 1 {
			t.Fatal("mail FTS/public rowid changed")
		}
		must0(s.Close())
	}
}

func TestNotesCompletePartialRetainsRepresentationsAndStableIDs(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := beginNotes(t, s, "A", base)
	commitNotes(t, s, first, "complete", "bytes-A", noteFixture("notebook", "Original", "original searchable"))
	rows, _, err := s.Notes(ctx, NoteFilter{Query: "searchable"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial notes: %#v/%v", rows, err)
	}
	id, source := rows[0].ID, rows[0].SourceID
	detail, err := s.Note(ctx, id)
	if err != nil || len(detail.Elements) != 2 || len(detail.Flags) != 1 || len(detail.Ancestors) != 2 {
		t.Fatalf("native evidence: %#v/%v", detail, err)
	}
	if detail.Elements[0].EntityRowID != 1 || detail.Elements[0].GOID != "paragraph" {
		t.Fatal("native element parent ID was replaced by an archive rowid")
	}
	partial := noteFixture("notebook", "Damaged", "lost previous text")
	partial.Elements, partial.Flags = partial.Elements[:1], nil
	partial.Losses = []onenote.Loss{{Code: "onenote_element_unmapped", Count: 1}}
	partial.Entities = append(partial.Entities, onenote.Entity{RowID: 9, Type: 1, GOID: "newpage", GUID: "newguid", Title: "New mapped page"})
	partial.Elements = append(partial.Elements, onenote.Element{RowID: 99, EntityRowID: 9, GOID: "newtext", Text: "novelword", Kind: "rich_text", Jcid: 393230})
	commitNotes(t, s, beginNotes(t, s, "B", base.Add(time.Second)), "partial", "bytes-B", partial)
	detail, err = s.Note(ctx, id)
	if err != nil || detail.Title != "Original" || detail.CaptureID != first.ID ||
		len(detail.Elements) != 2 || len(detail.Flags) != 1 || detail.SourceID != source {
		t.Fatalf("partial erased prior representation: %#v/%v", detail, err)
	}
	rows, _, err = s.Notes(ctx, NoteFilter{Query: "picturewords"})
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatal("partial erased old OCR search hit")
	}
	rows, _, err = s.Notes(ctx, NoteFilter{Query: "novelword"})
	if err != nil || len(rows) != 1 {
		t.Fatal("partial did not admit previously unrepresented page")
	}
	sources, err := s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 || sources[0].Status != "partial" ||
		sources[0].Losses["onenote_element_unmapped"] != 1 {
		t.Fatalf("partial coverage lost: %#v/%v", sources, err)
	}
	commitNotes(t, s, beginNotes(t, s, "C", base.Add(2*time.Second)), "complete", "bytes-C", noteFixture("notebook", "Repaired", "new searchable"))
	detail, err = s.Note(ctx, id)
	if err != nil || detail.Title != "Repaired" || detail.SourceID != source {
		t.Fatal("complete replacement changed stable note/source ID")
	}
	rows, _, err = s.Notes(ctx, NoteFilter{Query: "novelword"})
	if err != nil || len(rows) != 1 {
		t.Fatal("absent page incorrectly treated as native deletion")
	}
}

func TestNotesOrderedAdmissionAndLateFailureHistory(t *testing.T) {
	ctx := context.Background()
	for _, statuses := range [][2]string{{"complete", "complete"}, {"complete", "partial"}, {"partial", "complete"}, {"partial", "partial"}} {
		t.Run(statuses[0]+"-"+statuses[1], func(t *testing.T) {
			s := newStore(t)
			old := beginNotes(t, s, "old", base)
			newer := beginNotes(t, s, "new", base.Add(time.Second))
			result := noteFixture("notebook", "Newer", "winner")
			if statuses[1] == "partial" {
				result.Losses = []onenote.Loss{{Code: "onenote_element_unmapped", Count: 1}}
			}
			commitNotes(t, s, newer, statuses[1], "new-bytes", result)
			oldResult := noteFixture("notebook", "Older", "loser")
			if statuses[0] == "partial" {
				oldResult.Losses = []onenote.Loss{{Code: "onenote_element_unmapped", Count: 2}}
			}
			err := s.CommitNotes(ctx, old, KnowledgeOutcome{Status: statuses[0], ContentDigest: "old-bytes", At: base}, oldResult)
			if !errors.Is(err, ErrStaleKnowledgeAttempt) {
				t.Fatalf("late writer accepted: %v", err)
			}
			rows, _, err := s.Notes(ctx, NoteFilter{})
			if err != nil || len(rows) != 1 || rows[0].Title != "Newer" {
				t.Fatal("late completion overwrote winner")
			}
			if n := rowCount(t, s, `select count(*) from knowledge_attempts where id=? and finished_at is not null`, old.ID); n != 1 {
				t.Fatal("stale terminal history rolled back")
			}
		})
	}
	s := newStore(t)
	old := beginNotes(t, s, "A", base)
	newer := beginNotes(t, s, "B", base.Add(time.Second))
	commitNotes(t, s, newer, "complete", "B", noteFixture("notebook", "Newer", "winner"))
	if err := s.RecordKnowledgeFailure(ctx, old, KnowledgeOutcome{Status: "cancelled", Reason: "onenote_snapshot_unavailable", At: base}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("late cancellation accepted: %v", err)
	}
	sources, err := s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 || sources[0].AttemptID != newer.ID || sources[0].Status != "complete" {
		t.Fatal("late failure poisoned current diagnostics")
	}
}

func TestNotesUnchangedInheritsPartialAndNeverAdmittedRefuses(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := beginNotes(t, s, "A", base)
	result := noteFixture("notebook", "Partial", "retained")
	result.Losses = []onenote.Loss{{Code: "onenote_modified_unknown", Count: 1}}
	commitNotes(t, s, first, "partial", "same", result)
	next := beginNotes(t, s, "A", base.Add(time.Second))
	commitNotes(t, s, next, "unchanged", "same", onenote.Result{})
	sources, err := s.KnowledgeSources(ctx, "notes")
	if err != nil || sources[0].Status != "unchanged" || sources[0].Coverage != "partial" ||
		sources[0].Losses["onenote_modified_unknown"] != 1 || sources[0].CompleteAt != nil {
		t.Fatalf("unchanged invented complete coverage: %#v/%v", sources, err)
	}
	rows, _, err := s.Notes(ctx, NoteFilter{})
	if err != nil || rows[0].CaptureID != first.ID {
		t.Fatal("unchanged rewrote content capture clock")
	}
	empty := newStore(t)
	attempt := beginNotes(t, empty, "same", base)
	if err := empty.CommitNotes(ctx, attempt, KnowledgeOutcome{Status: "unchanged", ContentDigest: "same", At: base}, onenote.Result{}); err == nil {
		t.Fatal("never-admitted equality accepted")
	}
}

func TestNotesNotebookContinuityAndScopedReset(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	commitNotes(t, s, beginNotes(t, s, "A", base), "complete", "A", noteFixture("notebook-A", "First", "first"))
	initial, _, _ := s.Notes(ctx, NoteFilter{})
	initialSource := initial[0].SourceID
	churn := noteFixture("notebook-A", "Renamed", "changed")
	churn.Entities[2].GOID = "different-page"
	commitNotes(t, s, beginNotes(t, s, "B", base), "complete", "B", churn)
	sources, err := s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 || sources[0].ID != initialSource {
		t.Fatal("page churn changed notebook instance")
	}
	missing := noteFixture("untrusted-new-notebook", "Partial", "partial")
	missing.Entities = missing.Entities[1:]
	missing.Losses = []onenote.Loss{{Code: "onenote_entity_unmapped", Count: 1}}
	commitNotes(t, s, beginNotes(t, s, "C", base), "partial", "C", missing)
	sources, err = s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 {
		t.Fatal("partial missing boundary inferred reset")
	}
	commitNotes(t, s, beginNotes(t, s, "D", base), "complete", "D", noteFixture("notebook-B", "Reset", "different"))
	sources, err = s.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 2 {
		t.Fatal("genuinely conflicting notebook evidence failed to create incarnation")
	}
	oldRows, _, err := s.Notes(ctx, NoteFilter{SourceID: initialSource})
	if err != nil || len(oldRows) != 2 {
		t.Fatal("reset erased prior scoped notes")
	}
}
