package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestNotesRefreshFailureReturnsRetainedContentWithExplicitDiagnostic(t *testing.T) {
	for _, cause := range []error{errs.DBError(errors.New("synthetic refresh fault")), errors.New("synthetic refresh fault")} {
		t.Run(cause.Error(), func(t *testing.T) {
			e := newEnv(t)
			seedNotesCLI(t, e)
			run := runNotesSync
			t.Cleanup(func() { runNotesSync = run })
			runNotesSync = func(context.Context, syncer.NotesOptions) (syncer.NotesReport, error) {
				return syncer.NotesReport{}, cause
			}
			for _, format := range []string{"json", "text"} {
				code, stdout, stderr := e.run("--format", format, "notes", "list")
				if code != 0 {
					t.Fatal(stderr)
				}
				if format == "text" {
					if !strings.Contains(stdout, "notes refresh error") || !strings.Contains(stdout, "Quarterly") {
						t.Fatal("text concealed failed refresh or lost retained content")
					}
				} else {
					doc := decode(t, stdout)
					if len(items(t, doc)) != 1 || doc["source_coverage"].(map[string]any)["refresh_error"] == nil {
						t.Fatal("retained read concealed refresh error")
					}
				}
			}
		})
	}
}

func TestNotesRefreshArchiveAndCancellationFailuresRemainErrors(t *testing.T) {
	for _, failure := range []string{"no-archive", "bad-archive", "bad-source-schema", "missing-selection", "cancelled", "deadline", "sync"} {
		t.Run(failure, func(t *testing.T) {
			e := newEnv(t)
			if failure != "no-archive" {
				seedNotesCLI(t, e)
			}
			switch failure {
			case "bad-archive":
				if err := os.WriteFile(e.db, []byte("not SQLite"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "bad-source-schema":
				e.exec(`alter table knowledge_sources rename column boundary to broken`)
			}
			run := runNotesSync
			t.Cleanup(func() { runNotesSync = run })
			runNotesSync = func(context.Context, syncer.NotesOptions) (syncer.NotesReport, error) {
				if failure == "cancelled" {
					return syncer.NotesReport{}, context.Canceled
				}
				if failure == "deadline" {
					return syncer.NotesReport{}, context.DeadlineExceeded
				}
				return syncer.NotesReport{}, errors.New("synthetic refresh fault")
			}
			args := []string{"notes", "list"}
			if failure == "missing-selection" {
				args = append(args, "--source", "999")
			}
			if failure == "sync" {
				args = []string{"notes", "sync"}
			}
			if code, _, stderr := e.run(args...); code == 0 {
				t.Fatalf("terminal failure concealed: %s", stderr)
			}
		})
	}
	if got := noteStamp(nil); got != "-" {
		t.Fatal("unknown note time invented")
	}
}
