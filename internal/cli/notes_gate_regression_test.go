package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestSigninArchiveTableQueryErrorsAreNotMissingHostSuccess(t *testing.T) {
	e := trEnv(t)
	original := signinTables
	t.Cleanup(func() { signinTables = original })
	for _, cause := range []error{context.Canceled, errors.New("synthetic table query fault")} {
		signinTables = func(*store.Store, context.Context) (bool, error) { return false, cause }
		rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
		rt.dbPath = e.db
		_, _, err := rt.signinTarget("")
		if !errors.Is(err, cause) {
			t.Fatalf("table error became no-host usage: %v", err)
		}
	}
}

func TestNotesLegacyFreshnessErrorBranchesRemainObservable(t *testing.T) {
	e := newEnv(t)
	e.garbageArchive()
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	rt.dbPath = e.db
	if _, _, err := rt.lastSuccess(); err == nil {
		t.Fatal("legacy unreadable archive became healthy clock")
	}
	rt.maxAge = 0
	if err := rt.read("messages", func(*store.Store) (result, error) {
		t.Fatal("corrupt archive must not call read consumer")
		return nil, nil
	}); err == nil {
		t.Fatal("legacy read concealed archive error")
	}
}

func TestNotesSharedSearchProjectionAndRefreshErrorsAreExplicit(t *testing.T) {
	e := newEnv(t)
	seedNotesCLI(t, e)
	e.exec(`alter table note_entities rename column title to broken`)
	if code, _, stderr := e.run("--max-age", "0", "search", "Quarterly", "--source", "notes"); code == 0 {
		t.Fatal(stderr)
	}
	e.garbageArchive()
	if code, _, stderr := e.run("search", "Quarterly"); code == 0 {
		t.Fatal(stderr)
	}
}

func TestNotesSharedSearchNoHitsAndTextSourceFollowUp(t *testing.T) {
	e := newEnv(t)
	seedNotesCLI(t, e)
	for _, args := range [][]string{
		{"search", "not-present"},
		{"search", "not-present", "--source", "notes"},
		{"--format", "text", "search", "Quarterly", "--source", "notes"},
		{"--format", "text", "notes", "sources"},
	} {
		if code, _, stderr := e.run(append([]string{"--max-age", "0"}, args...)...); code != 0 {
			t.Fatal(stderr)
		}
		empty := newEnv(t)
		if code, stdout, stderr := empty.run("--format", "text", "--max-age", "0", "notes", "list"); code != 0 || !bytes.Contains([]byte(stdout), []byte("no notes captured")) {
			t.Fatalf("unknown source text: %d/%s/%s", code, stdout, stderr)
		}
	}
}

func TestNotesCoverageNilEmitsNoInventedState(t *testing.T) {
	var out bytes.Buffer
	rt := newRuntime(context.Background(), &Globals{}, &out, &bytes.Buffer{})
	rt.renderNoteCoverage(nil)
	if out.Len() != 0 {
		t.Fatal("nonparticipating source emitted coverage")
	}
}
