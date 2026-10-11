package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestNotesReviewedStaleAdmissionKeepsNewerTerminalState(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := notesIndex(t, root, "index.db", "text")
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	read := notesRead
	t.Cleanup(func() { notesRead = read })
	notesRead = func(ctx context.Context, copyPath string) (onenote.Result, error) {
		result, err := read(ctx, copyPath)
		if err != nil {
			return result, err
		}
		newer, err := st.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "notes", Platform: notesPlatform, Locator: path, At: time.Now().UTC()})
		if err != nil {
			return result, err
		}
		err = st.RecordKnowledgeFailure(ctx, newer, store.KnowledgeOutcome{Status: "failed", At: time.Now().UTC(), Reason: "newer_failure"})
		return result, err
	}
	if _, err := syncNotes(ctx, st, NotesOptions{Root: root, Explicit: true, Discover: true}); !errors.Is(err, store.ErrStaleKnowledgeAttempt) {
		t.Fatalf("stale admission was not refused: %v", err)
	}
	sources, err := st.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 1 || sources[0].AttemptID != 2 || sources[0].Reason != "newer_failure" {
		t.Fatal("stale admission poisoned newer terminal diagnostic", err)
	}
}
