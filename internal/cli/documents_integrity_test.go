package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsCLIArchiveAndProjectionFailuresRemainErrors(t *testing.T) {
	for _, command := range [][]string{{"docs", "list"}, {"docs", "search", "Quarterly"}, {"docs", "show", "document-observation:1"}} {
		e := newEnv(t)
		seedDocumentsCLI(t, e)
		e.exec(`alter table document_observations rename column title to broken`)
		code, _, stderr := e.run(append([]string{"--no-sync"}, command...)...)
		if code == 0 || !strings.Contains(stderr, "db_error") {
			t.Fatalf("broken document projection became empty success: %v %s", command, stderr)
		}
	}
	e := newEnv(t)
	e.garbageArchive()
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	for _, cause := range []error{nil, errors.New("refresh failed")} {
		runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
			return syncer.DocumentReport{}, cause
		}
		if code, _, stderr := e.run("docs", "list"); code == 0 || !strings.Contains(stderr, "db_error") {
			t.Fatal("corrupt archive became retained healthy data")
		}
	}
}

func TestDocumentsCLIReadOnlyArchiveBytesAndTimesStayUnchanged(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"docs", "list"}, {"docs", "search", "Quarterly"},
		{"docs", "show", "document-observation:1"}, {"docs", "sources"}, {"search", "Quarterly", "--source", "documents"}} {
		if code, _, stderr := e.run(append([]string{"--no-sync"}, command...)...); code != 0 {
			t.Fatal(stderr)
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(e.db)
	if err != nil || !bytes.Equal(before, after) || !current.ModTime().Equal(info.ModTime()) {
		t.Fatal("archive-only document reads changed archive bytes/mtime")
	}
}

func TestDocumentsCLISelectionDuringRefreshCannotHideCorruption(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		e.exec(`alter table document_observations rename column title to broken`)
		return syncer.DocumentReport{}, nil
	}
	if code, _, stderr := e.run("--no-sync", "docs", "show", id); code == 0 || !strings.Contains(stderr, "db_error") {
		t.Fatal("detail hid corruption introduced between lookup and selected refresh")
	}
}

func TestDocumentsCLIReadCallbackFailureIsReportedWithoutArchive(t *testing.T) {
	rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
	rt.dbPath = filepath.Join(t.TempDir(), "absent.db")
	rt.maxAge = 0
	cause := store.ErrDocumentObservationNotFound
	if err := rt.documentsRead("docs show", 0, func(st *store.Store) (*listResult, error) {
		if st != nil {
			t.Fatal("absent archive acquired a writable store")
		}
		return nil, cause
	}); err == nil {
		t.Fatal("detail callback error became an empty successful result")
	}
}
