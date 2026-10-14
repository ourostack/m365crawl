package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
)

func TestLocalInsertResultFailureRollsBackIndexAndMembership(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, "", "")
	a := beginLocal(t, s, "docx", base)
	affected := localInsertAffected
	t.Cleanup(func() { localInsertAffected = affected })
	localInsertAffected = func(sql.Result) (int64, error) { return 0, errors.New("synthetic affected-row failure") }
	if err := s.CommitLocalText(context.Background(), a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("insert result failure ignored")
	}
	if rowCount(t, s, `select count(*) from local_text_observations`)+rowCount(t, s, `select count(*) from local_text_capture_members`)+rowCount(t, s, `select count(*) from local_text_fts`) != 0 {
		t.Fatal("failed index publication leaked")
	}
	localInsertAffected = affected
	if err := s.CommitLocalText(context.Background(), a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalWindowsIndexLabelValidatesOnAnyReaderHost(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, `<w:p><w:r><w:t>realword</w:t></w:r></w:p>`, "")
	created, modified := "116444736010000000", "116444736020000000"
	c.Evidence.NativeGenerationRaw = nil
	c.Evidence.CreationRaw, c.Evidence.ModificationRaw = &created, &modified
	c.Evidence.GenerationKind = "native_creation"
	input := officecapture.TextInput{Path: `C:\synthetic\file.docx`, RequestedKind: "docx"}
	a, err := s.BeginLocalTextAttempt(context.Background(), KnowledgeInput{Kind: LocalTextKind, Platform: "windows", Locator: LocalTextLocator(input), At: base}, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLocalText(context.Background(), a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{Query: "realword"})
	if err != nil || len(snap.Rows) != 1 {
		t.Fatalf("native label interpreted as reader-host path: %v", err)
	}
	if localTextLabel(officecapture.TextInput{Path: `/synthetic/file\name.docx`}, "darwin") != `file\name.docx` {
		t.Fatal("Darwin literal backslash treated as a separator")
	}
}

func TestLocalCompleteFrontierCannotPointAtAnotherInstance(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, "", "A")
	commitLocal(t, s, c, base)
	gen := "8"
	c.Evidence.NativeGenerationRaw = &gen
	commitLocal(t, s, c, base.Add(time.Second))
	if _, err := s.db.Exec(`update knowledge_sources set complete_attempt=2 where id=1`); err != nil {
		t.Fatal(err)
	}
	assertLocalCorruptionClosed(t, s)
}
