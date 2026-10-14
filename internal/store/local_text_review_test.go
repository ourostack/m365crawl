package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
)

func assertLocalCorruptionClosed(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, f := range []LocalTextFilter{{}, {Query: "absentword", Limit: 1}, {Query: "realword"}, {Query: "inventedword"}} {
		if _, err := s.LocalTextSnapshot(ctx, f); err == nil {
			t.Fatalf("corruption hidden in filter %#v", f)
		}
	}
	if _, err := s.LocalTextSources(ctx, 0); err == nil {
		t.Fatal("corrupt sources succeeded")
	}
	if _, err := s.LocalTextDetail(ctx, 1); err == nil {
		t.Fatal("corrupt detail succeeded")
	}
}

func TestLocalSuccessfulAttemptOutcomeAndCompleteAudit(t *testing.T) {
	for _, statement := range []string{
		`update knowledge_attempts set coverage='supported_text',outcome='complete',losses_json='{}' where id=1`,
		`update knowledge_attempts set losses_json='{"embedded_content_unmapped":999}' where id=2`,
		`update knowledge_attempts set coverage='supported_text' where id=2`,
		`update knowledge_sources set complete_attempt=999`,
		`update knowledge_sources set complete_attempt=2`,
		`update knowledge_attempts set losses_json='not-json' where id=2`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			c := localCapture(t, `<w:p><w:r><w:t>realword</w:t><w:object/></w:r></w:p>`, "")
			if !c.Result.Partial || len(c.Result.Losses) != 1 || c.Result.Losses[0].Count != 1 {
				t.Fatal("production partial fixture")
			}
			for i := range 3 {
				commitLocal(t, s, c, base.Add(time.Duration(i)*time.Second))
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			assertLocalCorruptionClosed(t, s)
		})
	}
}

func TestLocalCompleteFrontierAndLatestFailureAreIndependent(t *testing.T) {
	s := newStore(t)
	complete := localCapture(t, `<w:p><w:r><w:t>realword</w:t></w:r></w:p>`, "")
	commitLocal(t, s, complete, base)
	partial := localCapture(t, `<w:p><w:r><w:t>realword</w:t><w:object/></w:r></w:p>`, "")
	commitLocal(t, s, partial, base.Add(time.Second))
	ctx := context.Background()
	a := beginLocal(t, s, "docx", base.Add(2*time.Second))
	if err := s.RecordLocalTextFailure(ctx, a, KnowledgeOutcome{Status: "failed", Reason: "local_text_too_large", At: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	sources, err := s.LocalTextSources(ctx, 0)
	if err != nil || len(sources) != 1 || sources[0].Source.Status != "failed" || sources[0].Source.Coverage != "partial" ||
		sources[0].Source.Losses["embedded_content_unmapped"] != 1 || sources[0].Source.CompleteAt == nil {
		t.Fatalf("diagnostic replaced admitted coverage: %#v %v", sources, err)
	}
	for _, statement := range []string{
		`update knowledge_sources set complete_attempt=null`,
		`update knowledge_sources set complete_attempt=2`,
		`update knowledge_sources set complete_attempt=3`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		assertLocalCorruptionClosed(t, s)
		if _, err := s.db.Exec(`update knowledge_sources set complete_attempt=1`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalTypedFTSBindingFailsClosedAndReuseNeverRepairs(t *testing.T) {
	for _, statement := range []string{
		`delete from local_text_fts`,
		`update local_text_fts set text='inventedword'`,
		`update local_text_fts set label='wrong-file'`,
		`update local_text_fts set metadata='/different/file.docx'`,
		`insert into local_text_fts(rowid,label,text,metadata) values(999,'orphan','inventedword','/orphan')`,
		`delete from local_text_fts; insert into local_text_fts(rowid,label,text,metadata) values(1,'wrong','inventedword','/wrong')`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			c := localCapture(t, `<w:p><w:r><w:t>realword</w:t></w:r></w:p>`, "")
			commitLocal(t, s, c, base)
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			assertLocalCorruptionClosed(t, s)
			a := beginLocal(t, s, "docx", base.Add(time.Second))
			if err := s.CommitLocalText(context.Background(), a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err == nil {
				t.Fatal("reuse repaired or accepted corrupt index")
			}
		})
	}
}

func TestLocalYearOneRawModifiedClockRemainsNullAndNeverPanics(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, `<w:p><w:r><w:t>realword</w:t></w:r></w:p>`, "")
	raw := "-62135596800:0"
	zero := time.Unix(-62135596800, 0).UTC()
	c.Evidence.ModificationRaw, c.Evidence.ModifiedAt = &raw, &zero
	if err := s.CommitLocalText(context.Background(), beginLocal(t, s, "docx", base), LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("nonnull year-one sentinel admitted")
	}
	c.Evidence.ModifiedAt = nil
	commitLocal(t, s, c, base.Add(time.Second))
	ctx := context.Background()
	for _, f := range []LocalTextFilter{{}, {Query: "absentword"}, {ModifiedSince: base}} {
		snap, err := s.LocalTextSnapshot(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if f.ModifiedSince.IsZero() && len(snap.Sources) != 1 {
			t.Fatal("sentinel source missing")
		}
	}
	detail, err := s.LocalTextDetail(ctx, 1)
	if err != nil || detail.Observation.Snapshot.Evidence.ModifiedAt != nil || *detail.Observation.Snapshot.Evidence.ModificationRaw != raw {
		t.Fatal("raw/null clock changed")
	}
	if _, err := s.LocalTextSources(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update local_file_snapshots set evidence_json=json_set(evidence_json,'$.ModifiedAt','0001-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	assertLocalCorruptionClosed(t, s)
}

func localLongPartCapture(t *testing.T) LocalTextCapture {
	t.Helper()
	part := "word/" + strings.Repeat("p", 32769) + ".xml"
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, member := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="main" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body><w:sectPr><w:headerReference r:id="header"/></w:sectPr></w:body></w:document>`},
		{"word/_rels/document.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="header" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="/` + part + `"/></Relationships>`},
		{part, `<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>longpartword</w:t></w:r></w:p></w:hdr>`},
	} {
		w, err := z.Create(member[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(member[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := documenttext.Extract(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()), "docx", documenttext.DefaultLimits())
	if err != nil || len(result.Paragraphs) != 1 || result.Paragraphs[0].Part != part {
		t.Fatalf("producer long part: %v", err)
	}
	c := localCapture(t, "", "")
	c.Result, c.SourceByteDigest, c.Evidence.Size = result, fmt.Sprintf("%x", sha256.Sum256(b.Bytes())), int64(b.Len())
	return c
}

func TestLocalProductionLongPartPreservedOffline(t *testing.T) {
	c := localLongPartCapture(t)
	s := newStore(t)
	commitLocal(t, s, c, base)
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{Query: "longpartword"})
	if err != nil || len(snap.Rows) != 1 || snap.Rows[0].Capture.Result.Paragraphs[0].Part != c.Result.Paragraphs[0].Part {
		t.Fatalf("long part admission/read changed: %v", err)
	}
	var full string
	if err := s.db.QueryRow(`select result_json from local_text_observations where rowid=1`).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, c.Result.Paragraphs[0].Part) {
		t.Fatal("full record lost long native part")
	}
}
