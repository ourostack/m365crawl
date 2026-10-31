package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
)

func documentRecord(at time.Time, outcome, code, text string) WorkIQDocumentRecord {
	allowed, label, name := "allowed", "9fbde396-1a24-4c79-8edf-9254a0f35055", `Confidential\Internal Only`
	encrypted := true
	r := WorkIQDocumentRecord{Account: "tenant/user", DriveID: "b!drive0001", ItemID: "ITEM0001AB", Name: "Plan.docx",
		WebURL: "https://contoso.sharepoint.com/sites/s/Plan.docx", Text: text, Extracts: 1,
		Check: WorkIQDocumentCheck{CheckedAt: at, Outcome: outcome, Code: code, ContentProtection: &allowed, LabelID: &label,
			LabelName: &name, LabelEncrypted: &encrypted, BytesState: "not_inspected", RightsSource: "workiq_retrieve",
			Qualification: WorkIQDocumentQualification}}
	if outcome == "admitted" {
		r.Check.Attribution = "label"
	}
	return r
}

func TestWorkIQDocumentAdmitThenRefuseDeletesBody(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	t0 := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	w, err := s.RecordWorkIQDocument(ctx, documentRecord(t0, "admitted", "", "Purple walrus marmalade sentinel"))
	if err != nil || w.DocumentID <= 0 || w.CheckID <= 0 || w.BodyDeleted {
		t.Fatalf("admit: %+v %v", w, err)
	}
	hits, err := s.SearchWorkIQDocuments(ctx, "walrus", 10)
	if err != nil || len(hits) != 1 || hits[0].Body == nil || hits[0].Body.Text != "Purple walrus marmalade sentinel" ||
		hits[0].Last.LabelEncrypted == nil || !*hits[0].Last.LabelEncrypted || hits[0].Last.Attribution != "label" {
		t.Fatalf("search: %+v %v", hits, err)
	}

	refused := documentRecord(t0.Add(time.Hour), "refused", "document_extract_not_allowed", "")
	blocked := "blocked"
	refused.Check.ContentProtection = &blocked
	w2, err := s.RecordWorkIQDocument(ctx, refused)
	if err != nil || w2.DocumentID != w.DocumentID || !w2.BodyDeleted {
		t.Fatalf("refuse: %+v %v", w2, err)
	}
	if hits, err := s.SearchWorkIQDocuments(ctx, "walrus", 10); err != nil || len(hits) != 0 {
		t.Fatalf("refused text still searchable: %+v %v", hits, err)
	}
	if n := rowCount(t, s, `select count(*) from workiq_document_bodies`); n != 0 {
		t.Fatalf("bodies=%d", n)
	}
	d, checks, err := s.WorkIQDocumentDetail(ctx, w.DocumentID, 20)
	if err != nil || d.Body != nil || d.Last.Outcome != "refused" || d.Last.Code != "document_extract_not_allowed" || len(checks) != 2 ||
		checks[0].ID != w2.CheckID || checks[1].Outcome != "admitted" || !d.FirstCheckedAt.Equal(t0) || !d.LastCheckedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("detail: %+v %+v %v", d, checks, err)
	}

	// A later admission restores a body from that check only.
	w3, err := s.RecordWorkIQDocument(ctx, documentRecord(t0.Add(2*time.Hour), "admitted", "", "Second text"))
	if err != nil || w3.BodyDeleted {
		t.Fatal(err)
	}
	docs, err := s.WorkIQDocuments(ctx)
	if err != nil || len(docs) != 1 || docs[0].Body == nil || docs[0].Body.CheckID != w3.CheckID || docs[0].Body.Text != "Second text" {
		t.Fatalf("documents: %+v %v", docs, err)
	}
}

func TestWorkIQDocumentRecordValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	for name, r := range map[string]WorkIQDocumentRecord{
		"refused with text":    documentRecord(at, "refused", "document_rights_unknown", "leak"),
		"refused without code": documentRecord(at, "refused", "", ""),
		"admitted with code":   documentRecord(at, "admitted", "document_rights_unknown", "x"),
		"unknown outcome":      documentRecord(at, "maybe", "", ""),
		"no account":           func() WorkIQDocumentRecord { r := documentRecord(at, "admitted", "", "x"); r.Account = ""; return r }(),
		"no time":              documentRecord(time.Time{}, "admitted", "", "x"),
	} {
		if _, err := s.RecordWorkIQDocument(ctx, r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if n := rowCount(t, s, `select count(*) from workiq_documents`); n != 0 {
		t.Fatalf("rejected records wrote %d documents", n)
	}
}

func TestWorkIQDocumentsOnOlderSchemaAndCorruptBody(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	w, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `update workiq_document_bodies set text='changed'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WorkIQDocumentDetail(ctx, w.DocumentID, 20); err == nil {
		t.Fatal("a body that no longer matches its digest was read")
	}
	if _, _, err := s.WorkIQDocumentDetail(ctx, w.DocumentID+1, 20); !errors.Is(err, ErrWorkIQDocumentUnknown) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `delete from schema_migrations where version>=23`); err != nil {
		t.Fatal(err)
	}
	if docs, err := s.WorkIQDocuments(ctx); err != nil || docs != nil {
		t.Fatalf("older schema: %+v %v", docs, err)
	}
	if _, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "x")); err == nil {
		t.Fatal("wrote document state below its schema")
	}
}

func TestWorkIQDocumentSchema23MigratesVersion22(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	cs, err := crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: strings.Replace(schemaDDL, workiqDocumentSchemaDDL, "", 1), SchemaVersion: 22})
	if err != nil {
		t.Fatal(err)
	}
	must0(cs.Close())
	old := must(OpenReadOnly(ctx, path))
	if docs, err := old.WorkIQDocuments(ctx); err != nil || docs != nil {
		t.Fatalf("version 22 read: %+v %v", docs, err)
	}
	if _, _, err := old.WorkIQDocumentDetail(ctx, 1, 20); !errors.Is(err, ErrWorkIQDocumentUnknown) {
		t.Fatalf("version 22 detail: %v", err)
	}
	must0(old.Close())
	s := must(Open(ctx, path))
	defer func() { _ = s.Close() }()
	if got := rowCount(t, s, `select max(version) from schema_migrations`); got != WorkIQDocumentSchemaVersion {
		t.Fatalf("version=%d", got)
	}
	if _, err := s.RecordWorkIQDocument(ctx, documentRecord(time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC), "admitted", "", "x")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkIQDocumentWriteFaults(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, setup string
		seed        bool
	}{
		{"document insert", `create trigger f before insert on workiq_documents begin select raise(fail,'x'); end`, false},
		{"check insert", `create trigger f before insert on workiq_document_checks begin select raise(fail,'x'); end`, false},
		{"body delete", `create trigger f before delete on workiq_document_bodies begin select raise(fail,'x'); end`, true},
		{"body insert", `create trigger f before insert on workiq_document_bodies begin select raise(fail,'x'); end`, false},
		{"document update", `create trigger f before update of last_check_id on workiq_documents begin select raise(fail,'x'); end`, false},
		{"schema state", `drop table schema_migrations`, false},
		{"incomplete tables", `drop table workiq_document_bodies`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			if tc.seed {
				if _, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "seed")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.ExecContext(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordWorkIQDocument(ctx, documentRecord(at.Add(time.Minute), "admitted", "", "text")); err == nil {
				t.Fatal("fault was not reported")
			}
		})
	}
	s := newStore(t)
	must0(s.Close())
	if _, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text")); err == nil {
		t.Fatal("closed archive accepted a write")
	}
}

func TestWorkIQDocumentReadFaults(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, setup string }{
		{"schema state", `drop table schema_migrations`},
		{"query", `drop table workiq_document_bodies; create table workiq_document_bodies(x)`},
		{"clock", `update workiq_documents set first_checked_at='bad'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			w, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			if _, err := s.WorkIQDocuments(ctx); err == nil {
				t.Fatal("list hid the fault")
			}
			if _, _, err := s.WorkIQDocumentDetail(ctx, w.DocumentID, 20); err == nil {
				t.Fatal("detail hid the fault")
			}
		})
	}
	s := newStore(t)
	w, err := s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text"))
	if err != nil {
		t.Fatal(err)
	}
	unlabeled := documentRecord(at.Add(time.Hour), "admitted", "", "text")
	unlabeled.Check.LabelID, unlabeled.Check.LabelName, unlabeled.Check.LabelEncrypted = nil, nil, nil
	if _, err := s.RecordWorkIQDocument(ctx, unlabeled); err != nil {
		t.Fatal(err)
	}
	if d, checks, err := s.WorkIQDocumentDetail(ctx, w.DocumentID, 20); err != nil || d.Last.LabelEncrypted != nil || checks[0].LabelEncrypted != nil {
		t.Fatalf("unlabeled check: %+v %v", d.Last, err)
	}
	if _, err := s.db.ExecContext(ctx, `update workiq_document_checks set checked_at='bad' where id=?`, w.CheckID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WorkIQDocumentDetail(ctx, w.DocumentID, 20); err == nil {
		t.Fatal("an unreadable older check was hidden")
	}
	if _, err := s.db.ExecContext(ctx, `alter table workiq_document_checks rename column qualification to renamed`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.workIQDocumentChecks(ctx, w.DocumentID, 20); err == nil {
		t.Fatal("a missing check table was hidden")
	}
	if _, err := s.SearchWorkIQDocuments(ctx, "***", 5); err == nil {
		t.Fatal("an empty query searched")
	}
}

func TestWorkIQDocumentRefusalLeavesNoTextOnDisk(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s := must(Open(ctx, path))
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ { // several segments, so the words are not only in the newest one
		r := documentRecord(at.Add(time.Duration(i)*time.Minute), "admitted", "", "zebrasecretword sentinel")
		r.ItemID = fmt.Sprintf("ITEM000%dAB", i)
		must(s.RecordWorkIQDocument(ctx, r))
	}
	for i := 0; i < 3; i++ {
		r := documentRecord(at.Add(time.Hour+time.Duration(i)*time.Minute), "refused", "document_extract_not_allowed", "")
		r.ItemID = fmt.Sprintf("ITEM000%dAB", i)
		w := must(s.RecordWorkIQDocument(ctx, r))
		if !w.BodyDeleted || w.LogPending {
			t.Fatalf("refusal %d: %+v", i, w)
		}
	}
	if n := rowCount(t, s, `select count(*) from workiq_document_fts_data where instr(block,'zebrasecretword')>0`); n != 0 {
		t.Fatalf("the index kept refused words in %d blocks", n)
	}
	if n := rowCount(t, s, `select count(*) from meta where key='`+purgedLogKey+`'`); n != 0 {
		t.Fatal("the log clear request was left behind")
	}
	must0(s.Close())
	for _, name := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(name) //nolint:gosec // G304: the test's own archive.
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("zebrasecretword")) {
			t.Fatalf("%s still holds refused text", filepath.Base(name))
		}
	}
}

func TestWorkIQDocumentLogClearFaults(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	s := newStore(t)
	must(s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text")))
	must(s.db.ExecContext(ctx, `create trigger f before insert on meta begin select raise(fail,'x'); end`))
	if _, err := s.RecordWorkIQDocument(ctx, documentRecord(at.Add(time.Minute), "admitted", "", "new")); err == nil {
		t.Fatal("an unrecorded log clear request was hidden")
	}
	must(s.db.ExecContext(ctx, `drop trigger f; create trigger f before delete on meta begin select raise(fail,'x'); end`))
	w, err := s.RecordWorkIQDocument(ctx, documentRecord(at.Add(2*time.Minute), "refused", "document_rights_unknown", ""))
	if err == nil || !w.LogPending || !w.BodyDeleted {
		t.Fatalf("a failed log clear was hidden: %+v %v", w, err)
	}
}

func TestWorkIQDocumentLogClearIgnoresExpiredCallerDeadline(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	s := newStore(t)
	must(s.RecordWorkIQDocument(ctx, documentRecord(at, "admitted", "", "text")))
	// Cancelling right after commit stands in for a deadline that expires before the log clear.
	cctx, cancel := context.WithCancel(ctx)
	afterWorkIQDocumentCommit = cancel
	defer func() { afterWorkIQDocumentCommit = func() {} }()
	w, err := s.RecordWorkIQDocument(cctx, documentRecord(at.Add(time.Minute), "refused", "document_rights_unknown", ""))
	if err != nil || !w.BodyDeleted || w.LogPending {
		t.Fatalf("%+v %v", w, err)
	}
	if n := rowCount(t, s, `select count(*) from workiq_document_checks`); n != 2 {
		t.Fatalf("checks=%d", n)
	}
}
