package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func localReferenceFixture(t *testing.T, s *Store) []LocalTextReference {
	t.Helper()
	doc := documentFixture("Synthetic native reference", 0)
	doc.URL = "file:///synthetic/file.docx"
	other := doc
	other.Title = "Selected other remembered occurrence"
	other.Ordinal = 1
	admitDocument(t, s, beginDocument(t, s, base), base, "complete", "documents", []officedocuments.Document{doc, other})
	rows, _, err := s.DocumentObservations(context.Background(), DocumentFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatal("document fixture")
	}
	return []LocalTextReference{
		{DocumentObservationID: rows[0].ID, OriginalURL: doc.URL, AssociationState: "reference_locator_match"},
		{DocumentObservationID: rows[1].ID, OriginalURL: doc.URL, AssociationState: "reference_locator_match"},
	}
}

func TestLocalReferenceReuseAtomicAndPerAttempt(t *testing.T) {
	s := newStore(t)
	refs := localReferenceFixture(t, s)
	c := localCapture(t, `<w:p><w:r><w:t>referenceword</w:t></w:r></w:p>`, "")
	ctx := context.Background()
	for i, ref := range refs {
		if err := s.CommitLocalText(ctx, beginLocal(t, s, "docx", base.Add(time.Duration(i+1)*time.Second)), LocalTextCommit{Capture: c, RequestOrigin: "extension", Reference: &ref}); err != nil {
			t.Fatal(err)
		}
	}
	commitLocal(t, s, c, base.Add(3*time.Second))
	snap, err := s.LocalTextSnapshot(ctx, LocalTextFilter{})
	if err != nil || len(snap.Rows) != 1 {
		t.Fatal("representation not reused")
	}
	detail, err := s.LocalTextDetail(ctx, snap.Rows[0].ID)
	if err != nil || detail.ReferencesTotal != 2 || detail.References[0].AttemptID == detail.References[1].AttemptID ||
		detail.References[0].Reference.DocumentObservationID == detail.References[1].Reference.DocumentObservationID {
		t.Fatalf("origins overwritten: %#v %v", detail, err)
	}
	a := beginLocal(t, s, "docx", base.Add(4*time.Second))
	ref := refs[0]
	ref.OriginalURL = "file:///wrong.docx"
	if err := s.CommitLocalText(ctx, a, LocalTextCommit{Capture: c, RequestOrigin: "explicit", Reference: &ref}); err == nil {
		t.Fatal("contradictory reference committed")
	}
	if count := rowCount(t, s, `select count(*) from local_text_capture_members`); count != 3 {
		t.Fatal("rollback changed membership")
	}
	if _, err := s.db.Exec(`create trigger reference_failure before insert on local_text_references begin select raise(abort,'synthetic fault'); end`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLocalText(ctx, a, LocalTextCommit{Capture: c, RequestOrigin: "explicit", Reference: &refs[0]}); err == nil {
		t.Fatal("reference write fault hidden")
	}
	if count := rowCount(t, s, `select count(*) from local_text_capture_members`); count != 3 {
		t.Fatal("reference fault not atomic")
	}
}

func TestLocalReferenceReadBindingCorruption(t *testing.T) {
	for _, statement := range []string{
		`update local_text_references set attempt_id=999`,
		`update local_text_references set capture_id=999`,
		`update local_text_references set original_url='file:///wrong.docx'`,
		`update local_text_references set association_state='resolved'`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			refs := localReferenceFixture(t, s)
			c := localCapture(t, "", "")
			if err := s.CommitLocalText(context.Background(), beginLocal(t, s, "docx", base), LocalTextCommit{Capture: c, RequestOrigin: "explicit", Reference: &refs[0]}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LocalTextSources(context.Background(), 0); err == nil {
				t.Fatal("corrupt reference admitted")
			}
		})
	}
}

func TestLocalReferenceHistoryPreviewBound(t *testing.T) {
	s := newStore(t)
	refs := localReferenceFixture(t, s)
	c := localCapture(t, "", "")
	for i := range 101 {
		if err := s.CommitLocalText(context.Background(), beginLocal(t, s, "docx", base.Add(time.Duration(i)*time.Second)), LocalTextCommit{Capture: c, RequestOrigin: "explicit", Reference: &refs[0]}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.LocalTextDetail(context.Background(), snap.Rows[0].ID)
	if err != nil || detail.ReferencesTotal != 101 || len(detail.References) != 100 || !detail.ReferencesTruncated {
		t.Fatalf("history preview: %v", err)
	}
}
