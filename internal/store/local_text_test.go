package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
)

func localCapture(t *testing.T, text, extra string) LocalTextCapture {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, p := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + text + `</w:body></w:document>`},
		{"unselected.bin", extra},
	} {
		w, err := z.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	result, err := documenttext.Extract(context.Background(), bytes.NewReader(data), int64(len(data)), "docx", documenttext.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	gen, created, modified := "7", "1:0", "2:0"
	at := time.Unix(2, 0).UTC()
	kind := "docx"
	return LocalTextCapture{RequestedKind: kind, VerifiedKind: &kind, SourceByteDigest: fmt.Sprintf("%x", sha256.Sum256(data)),
		Evidence: officecapture.FileEvidence{VolumeID: "1", FileID: "2", GenerationPolicy: officecapture.FileGenerationPolicy,
			GenerationKind: "native_generation", GenerationQualified: true, NativeGenerationRaw: &gen, CreationRaw: &created, ModificationRaw: &modified, ModifiedAt: &at,
			Size: int64(len(data)), NativeAttributes: "0"}, Result: result, AcquiredAt: base}
}

func beginLocal(t *testing.T, s *Store, kind string, at time.Time) KnowledgeAttempt {
	t.Helper()
	path := "/synthetic/file.docx"
	input := officecapture.TextInput{Path: path, RequestedKind: kind}
	a, err := s.BeginLocalTextAttempt(context.Background(), KnowledgeInput{Kind: LocalTextKind, Platform: "darwin", Locator: LocalTextLocator(input), At: at}, input)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func commitLocal(t *testing.T, s *Store, c LocalTextCapture, at time.Time) {
	t.Helper()
	if err := s.CommitLocalText(context.Background(), beginLocal(t, s, c.RequestedKind, at), LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEqualTextDifferentFileBytes(t *testing.T) {
	s := newStore(t)
	a := localCapture(t, `<w:p><w:r><w:t>searchableword</w:t></w:r></w:p>`, "A")
	b := localCapture(t, `<w:p><w:r><w:t>searchableword</w:t></w:r></w:p>`, "B")
	commitLocal(t, s, a, base)
	commitLocal(t, s, b, base.Add(time.Second))
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
	if err != nil || len(snap.Rows) != 2 || snap.Rows[0].RepresentationDigest == snap.Rows[1].RepresentationDigest ||
		snap.Rows[0].Snapshot.SourceByteDigest == snap.Rows[1].Snapshot.SourceByteDigest || snap.Sources[0].SnapshotMatch != "matches" {
		t.Fatalf("bytes confused with projection: %#v %v", snap, err)
	}
	commitLocal(t, s, a, base.Add(2*time.Second))
	snap, err = s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
	if err != nil || len(snap.Rows) != 2 || snap.Rows[0].Retained || !snap.Rows[1].Retained {
		t.Fatalf("A-B-A: %#v %v", snap, err)
	}
}

func TestLocalCompoundKindIsOnlyIntent(t *testing.T) {
	for _, kind := range []string{"docx", "xlsx", "pptx"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			data := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
			result, err := documenttext.Extract(context.Background(), bytes.NewReader(data), 8, kind, documenttext.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			c := localCapture(t, "", "")
			c.RequestedKind, c.VerifiedKind, c.Result, c.SourceByteDigest = kind, nil, result, fmt.Sprintf("%x", sha256.Sum256(data))
			c.Evidence.Size = 8
			commitLocal(t, s, c, base)
			snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
			if err != nil || len(snap.Rows) != 1 || snap.Rows[0].Capture.VerifiedKind != nil || snap.Sources[0].LastGoodObservationID != nil {
				t.Fatalf("invented format/body: %#v %v", snap, err)
			}
		})
	}
}

func TestLocalSupportedEmptySupersedesBody(t *testing.T) {
	s := newStore(t)
	commitLocal(t, s, localCapture(t, `<w:p><w:r><w:t>old</w:t></w:r></w:p>`, ""), base)
	commitLocal(t, s, localCapture(t, "", ""), base.Add(time.Second))
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
	if err != nil || len(snap.Rows) != 2 {
		t.Fatal(err)
	}
	detail, err := s.LocalTextDetail(context.Background(), *snap.Sources[0].LastGoodObservationID)
	if err != nil || len(detail.Observation.Capture.Result.Paragraphs) != 0 || detail.Observation.Retained {
		t.Fatalf("empty not current: %#v %v", detail, err)
	}
}

func TestLocalNativeIncarnationIsolation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		s := newStore(t)
		c := localCapture(t, `<w:p><w:r><w:t>old</w:t></w:r></w:p>`, "")
		commitLocal(t, s, c, base)
		if unknown {
			c.Evidence.NativeGenerationRaw, c.Evidence.CreationRaw = nil, nil
			c.Evidence.GenerationKind, c.Evidence.GenerationQualified = "unavailable", false
		} else {
			gen := "8"
			c.Evidence.NativeGenerationRaw = &gen
		}
		commitLocal(t, s, c, base.Add(time.Second))
		commitLocal(t, s, c, base.Add(2*time.Second))
		snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
		want := 2
		if unknown {
			want = 3
		}
		if err != nil || len(snap.Sources) != want || !snap.Rows[0].Retained || snap.Sources[0].Source.Status != "retained" {
			t.Fatalf("instance reused: %#v %v", snap, err)
		}
	}
}

func TestLocalCorruptionFailsZeroHitAndSources(t *testing.T) {
	for _, sql := range []string{
		`update local_text_observations set representation_digest='wrong'`,
		`update local_file_snapshots set source_byte_digest='wrong'`,
		`update local_text_observations set verified_kind='xlsx'`,
		`update local_text_capture_members set observation_rowid=999`,
		`update local_text_locator_metadata set requested_kind='xlsx'`,
		`update local_text_source_state set current_snapshot_id=999`,
		`update knowledge_attempts set content_digest='wrong' where source_id is not null`,
		`update local_text_observations set result_json='not-json'`,
		`update local_text_observations set file_modified_at='wrong'`,
		`update local_file_snapshots set evidence_json='not-json'`,
		`update local_file_snapshots set acquired_at='invalid'`,
		`update local_file_snapshots set qualification='wrong'`,
		`update local_text_source_state set last_good_observation_id=999`,
		`update knowledge_sources set boundary='wrong'`,
		`update local_file_snapshots set evidence_json=json_set(evidence_json,'$.GenerationPolicy','unknown')`,
		`update local_file_snapshots set evidence_json=json_set(evidence_json,'$.NativeGenerationRaw',null,'$.GenerationKind','native_creation','$.CreationRaw','1')`,
		`delete from local_text_capture_members`,
		`update knowledge_locators set last_admitted=null`,
		`update knowledge_locators set latest_attempt=null`,
		`update knowledge_sources set admitted_attempt=null`,
	} {
		t.Run(sql, func(t *testing.T) {
			s := newStore(t)
			commitLocal(t, s, localCapture(t, "", ""), base)
			if _, err := s.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{Query: "nohit", Limit: 1}); err == nil {
				t.Fatal("corruption hidden by zero hits")
			}
			if _, err := s.LocalTextSources(context.Background(), 0); err == nil {
				t.Fatal("corruption hidden by sources")
			}
		})
	}
}

func TestLocalKindQualifiedRegistrationAndStale(t *testing.T) {
	s := newStore(t)
	wrong := beginLocal(t, s, "xlsx", base)
	correct := beginLocal(t, s, "docx", base)
	if wrong.LocatorID == correct.LocatorID {
		t.Fatal("kind registrations aliased")
	}

	request, err := s.LocalTextRequest(context.Background(), wrong.LocatorID)
	if err != nil || request.RequestedKind != "xlsx" {
		t.Fatal("request metadata changed")
	}
	c := localCapture(t, "", "")
	late := beginLocal(t, s, "docx", base.Add(time.Second))
	if err := s.CommitLocalText(context.Background(), correct, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("stale: %v", err)
	}
	if err := s.CommitLocalText(context.Background(), late, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReusedMembershipCompleteBinding(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, "", "")
	for i := range 3 {
		commitLocal(t, s, c, base.Add(time.Duration(i)*time.Second))
	}
	if _, err := s.db.Exec(`update knowledge_attempts set content_digest='wrong' where id=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{Query: "absent"}); err == nil {
		t.Fatal("intermediate reused capture corruption hidden")
	}
}
