package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestNotesReviewedEmbeddedFieldsAreSelectable(t *testing.T) {
	e := newEnv(t)
	id := seedNotesCLI(t, e)
	for _, args := range [][]string{
		{"notes", "show", id, "--fields", "id,title,text,capture_id"},
		{"search", "Quarterly", "--source", "notes", "--fields", "source,id,source_id,capture_id,modified_at"},
	} {
		code, stdout, stderr := e.run(append([]string{"--no-sync"}, args...)...)
		if code != 0 {
			t.Fatalf("embedded provenance field rejected: %d/%s", code, stderr)
		}
		doc := decode(t, stdout)
		if len(items(t, doc)) != 1 || doc["source_coverage"] == nil {
			t.Fatal("focused follow-up lost note or coverage")
		}
	}
}

func TestNotesReviewedProjectedTextHandlesDifferentTruncationKeys(t *testing.T) {
	e := newEnv(t)
	seedNotesCLI(t, e)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	attempt, err := st.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{
		Kind: "notes", Platform: "synthetic", Locator: "/fictional/second.db", At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = st.CommitNotes(context.Background(), attempt, store.KnowledgeOutcome{Status: "complete", ContentDigest: "different", At: at},
		onenote.Result{Entities: []onenote.Entity{{RowID: 1, Type: 1, GOID: "short", GUID: "short", Title: "Quarterly short", ModifiedAt: at}},
			Elements: []onenote.Element{{RowID: 1, EntityRowID: 1, GOID: "text", Text: "Short", Jcid: 393230, Kind: "rich_text"}}})
	closeErr := st.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	code, stdout, stderr := e.run("--format", "text", "--no-sync", "--max-text", "12",
		"--fields", "source,id,title,text", "search", "Quarterly", "--source", "notes")
	if code != 0 || !strings.Contains(stdout, "Quarterly short") || !strings.Contains(stdout, "text_truncated") {
		t.Fatalf("heterogeneous projected text failed: %d/%s/%s", code, stdout, stderr)
	}
}
