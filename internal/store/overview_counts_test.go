package store

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func TestOverviewCountsCountsEachSourceAndToleratesOldArchives(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	empty, err := s.OverviewCounts(ctx)
	if err != nil || empty != (OverviewCounts{}) {
		t.Fatalf("empty archive counts = %+v, %v", empty, err)
	}
	attempt := beginDocument(t, s, base)
	admitDocument(t, s, attempt, base, "complete", "counted", []officedocuments.Document{documentFixture("Counted", 0)})
	if _, err := s.db.ExecContext(ctx, `insert into people(tenant_id,id,display_name) values('t','8:orgid:a','A')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.OverviewCounts(ctx)
	if err != nil || got.Documents != 1 || got.TeamsPeople != 1 || got.Notes != 0 || got.PeopleObservations != 0 {
		t.Fatalf("counts = %+v, %v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `drop table document_observations`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OverviewCounts(ctx); err != nil || got.Documents != 0 {
		t.Fatalf("archive without the documents table = %+v, %v", got, err)
	}
}

func TestOverviewCountsReportsArchiveErrors(t *testing.T) {
	ctx := context.Background()
	for name, breakIt := range map[string]func(*Store) error{
		"count": func(s *Store) error {
			_, err := s.db.ExecContext(ctx, `drop table note_entities; create table note_entities(x)`)
			return err
		},
		"mail": func(s *Store) error {
			_, err := s.db.ExecContext(ctx, `drop table mail_messages; create table mail_messages(x)`)
			return err
		},
		"closed": func(s *Store) error { return s.db.Close() },
	} {
		s := newStore(t)
		if err := breakIt(s); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OverviewCounts(ctx); err == nil {
			t.Fatalf("%s: broken archive counted", name)
		}
	}
}
