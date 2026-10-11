package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestPeopleObservationsDetailUsesOneCommittedSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "archive.db")
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	first := setupPeopleObservation(t, writer)
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	published := false
	barrier := func() {
		if published {
			return
		}
		published = true
		a, err := writer.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
		if err != nil {
			t.Fatal(err)
		}
		err = writer.CommitPeopleObservations(ctx, a, KnowledgeOutcome{Status: "partial", ContentDigest: "two",
			Qualification: PeopleQualification, At: base}, outlookpeople.Result{
			People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "one@example.invalid", TeamsID: "conflicting"}},
			Losses: []outlookpeople.Loss{{Code: "outlook_blocks_damaged", Count: 1}},
		}, PeopleCaptureMetadata{})
		if err != nil {
			t.Fatal(err)
		}
	}
	connector := noteSnapshotConnector{faultConnector: faultConnector{dsn: sqliteFileURI(path, "mode=ro"),
		inj: &injector{seen: map[string]int{}}}, barrier: barrier, trigger: "where p.rowid="}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	reader.db = db
	detail, err := reader.PeopleObservation(ctx, 1)
	if err != nil || !published {
		t.Fatalf("snapshot barrier: %t %v", published, err)
	}
	if detail.CaptureID != first.ID || len(detail.Copies) != 1 || len(detail.Conflicts) != 0 || detail.Ambiguous || detail.Conflicting {
		t.Fatalf("mixed committed detail: %+v", detail)
	}
	fresh, err := writer.PeopleObservation(ctx, 1)
	if err != nil || len(fresh.Copies) != 2 || !fresh.Ambiguous || !fresh.Conflicting {
		t.Fatalf("writer publication missing: %+v %v", fresh, err)
	}
}
