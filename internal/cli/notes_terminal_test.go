package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestNotesReadProjectionFailuresAreNotEmptySuccess(t *testing.T) {
	for _, command := range []string{"list", "sources", "show", "open"} {
		t.Run(command, func(t *testing.T) {
			e := newEnv(t)
			id := seedNotesCLI(t, e)
			run := runNotesSync
			t.Cleanup(func() { runNotesSync = run })
			runNotesSync = func(context.Context, syncer.NotesOptions) (syncer.NotesReport, error) {
				switch command {
				case "sources":
					e.exec(`alter table knowledge_sources rename column boundary to broken`)
				case "open":
					if err := os.WriteFile(e.db, []byte("broken archive"), 0o600); err != nil {
						t.Fatal(err)
					}
				default:
					e.exec(`alter table note_entities rename column title to broken`)
				}
				return syncer.NotesReport{}, nil
			}
			args := []string{"notes", command}
			switch command {
			case "open":
				args = []string{"notes", "list"}
			case "show":
				args = append(args, id)
			}
			if code, _, stderr := e.run(args...); code == 0 || !strings.Contains(stderr, "db_error") {
				t.Fatalf("projection failure hidden: %d/%s", code, stderr)
			}
		})
	}
}

func TestNotesShowMissingAndDamagedArchiveIsExplicitError(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("--no-sync", "notes", "show", "note:1"); code == 0 {
		t.Fatal(stderr)
	}
	id := seedNotesCLI(t, e)
	e.exec(`alter table note_elements rename column text to broken`)
	if code, _, stderr := e.run("--no-sync", "notes", "show", id); code == 0 {
		t.Fatal(stderr)
	}
}

func TestNotesSourcesWithoutArchiveAndExplicitEmptyDirectory(t *testing.T) {
	e := newEnv(t)
	if code, stdout, stderr := e.run("--no-sync", "notes", "sources"); code != 0 || len(items(t, decode(t, stdout))) != 0 {
		t.Fatalf("no registered sources: %d/%s/%s", code, stdout, stderr)
	}
	code, stdout, stderr := e.run("notes", "sync", "--root", t.TempDir())
	if code != 0 || decode(t, stdout)["source_coverage"].(map[string]any)["state"] != "examined_empty_directory" {
		t.Fatalf("examined empty scope: %d/%s/%s", code, stdout, stderr)
	}
}
