package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

type documentPublicationReader struct {
	knowledgeReader
	publish func()
}

func (r *documentPublicationReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := r.knowledgeReader.QueryContext(ctx, query, args...)
	if err == nil && r.publish != nil && strings.Contains(query, "from document_observations d") {
		publish := r.publish
		r.publish = nil
		publish()
	}
	return rows, err
}

func TestDocumentSnapshotKeepsRowsAndSourcesInOneReadTransaction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	first := beginDocument(t, s, base)
	admitDocument(t, s, first, base, "complete", "first", []officedocuments.Document{documentFixture("Original", 0)})
	peer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var second KnowledgeAttempt
	reader := &documentPublicationReader{knowledgeReader: tx, publish: func() {
		second = beginDocument(t, peer, base.Add(time.Hour))
		admitDocument(t, peer, second, base.Add(time.Hour), "partial", "second",
			[]officedocuments.Document{documentFixture("Newer", 1)})
	}}
	snapshot, err := readDocumentSnapshot(ctx, reader, DocumentFilter{Limit: 1})
	if err != nil || len(snapshot.Rows) != 1 || snapshot.Truncated || len(snapshot.Sources) != 1 ||
		snapshot.Rows[0].CaptureID != first.ID || snapshot.Rows[0].Retained ||
		snapshot.Sources[0].AdmittedID != first.ID || snapshot.Sources[0].Status != "complete" {
		t.Fatalf("publication crossed snapshot boundaries: %+v %v", snapshot, err)
	}
	if second.ID == 0 {
		t.Fatal("separate publication did not occur between row and coverage reads")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := s.DocumentSnapshot(ctx, DocumentFilter{Limit: 1, SourceID: snapshot.Rows[0].SourceID})
	if err != nil || len(current.Rows) != 1 || !current.Truncated ||
		current.Sources[0].AdmittedID != second.ID || current.Sources[0].Status != "partial" {
		t.Fatalf("next command did not see committed publication: %+v %v", current, err)
	}
}

func TestDocumentSnapshotRefusesInvalidSourceAndClosedArchive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.DocumentSnapshot(ctx, DocumentFilter{SourceID: 999}); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown source became empty success: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DocumentSnapshot(ctx, DocumentFilter{}); err == nil {
		t.Fatal("closed archive became successful snapshot")
	}
}

func TestDocumentSnapshotSQLFaultsRemainTerminal(t *testing.T) {
	sweepFaults(t, func(t *testing.T, s *Store) { setupDocumentObservation(t, s) },
		func(ctx context.Context, s *Store) error {
			_, err := s.DocumentSnapshot(ctx, DocumentFilter{SourceID: 1, Query: "Original", Limit: 1})
			return err
		})
}
