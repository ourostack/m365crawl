package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
)

func TestSyncNotesPrivateReaderSharesBoundedCaptureDeadline(t *testing.T) {
	root := t.TempDir()
	notesIndex(t, root, "index.db", "fixture")
	read := notesRead
	t.Cleanup(func() { notesRead = read })
	notesRead = func(ctx context.Context, path string) (onenote.Result, error) {
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > 30*time.Second {
			return onenote.Result{}, errors.New("private reader did not receive bounded capture context")
		}
		return read(ctx, path)
	}
	report, err := SyncNotes(context.Background(), NotesOptions{
		DBPath: filepath.Join(t.TempDir(), "archive.db"), Root: root, Explicit: true, Discover: true,
	})
	if err != nil || report.Captured != 1 || report.Failed != 0 {
		t.Fatalf("reader deadline not shared: counts=%d/%d error=%v", report.Captured, report.Failed, err)
	}
}
