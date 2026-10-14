package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

func TestLocalCaptureFullFiniteValidation(t *testing.T) {
	c := localCapture(t, "", "")
	for _, mutate := range []func(*LocalTextCommit){
		func(c *LocalTextCommit) { c.RequestOrigin = "unknown" },
		func(c *LocalTextCommit) { c.Capture.AcquiredAt = time.Time{} },
		func(c *LocalTextCommit) { c.Capture.SourceByteDigest = "invalid" },
		func(c *LocalTextCommit) { c.Capture.RequestedKind = "pdf" },
		func(c *LocalTextCommit) { c.Capture.Evidence.VolumeID = "wrong" },
		func(c *LocalTextCommit) { c.Capture.Result.State = "invented" },
		func(c *LocalTextCommit) { c.Capture.VerifiedKind = nil },
		func(c *LocalTextCommit) { c.Capture.Result.State = "compound_unreadable" },
		func(c *LocalTextCommit) { c.Capture.Result.Cells = []documenttext.Cell{{}} },
		func(c *LocalTextCommit) {
			c.Capture.Result.Paragraphs = []documenttext.Paragraph{{Part: "../bad", Index: -1}}
		},
		func(c *LocalTextCommit) {
			c.Capture.Result.Paragraphs = []documenttext.Paragraph{{Part: "word/document.xml", Text: strings.Repeat("a", (4<<20)+1)}}
		},
		func(c *LocalTextCommit) {
			c.Capture.Result.Partial = true
			c.Capture.Result.Losses = []documenttext.Loss{{Code: "wrong", Count: 1}}
		},
		func(c *LocalTextCommit) {
			c.Reference = &LocalTextReference{DocumentObservationID: 0, OriginalURL: "url", AssociationState: "reference_locator_match"}
		},
		func(c *LocalTextCommit) {
			for range 1100 {
				c.Capture.Result.Paragraphs = append(c.Capture.Result.Paragraphs, documenttext.Paragraph{Part: strings.Repeat("p", 32768)})
			}
		},
	} {
		commit := LocalTextCommit{Capture: c, RequestOrigin: "explicit"}
		mutate(&commit)
		if err := validateLocalTextCommit(context.Background(), commit); err == nil {
			t.Fatal("invalid captured representation")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateLocalTextCommit(ctx, LocalTextCommit{}); err == nil {
		t.Fatal("cancelled validation")
	}
	x := c
	x.RequestedKind = "xlsx"
	kind := "xlsx"
	x.VerifiedKind = &kind
	x.Result.Kind = "xlsx"
	x.Result.Cells = []documenttext.Cell{{Part: "xl/worksheets/sheet1.xml", Reference: "A1", Type: "n", Value: "003.20", FormulaCached: true}}
	if err := validateLocalTextCommit(context.Background(), LocalTextCommit{Capture: x, RequestOrigin: "extension"}); err != nil {
		t.Fatal(err)
	}
	x.Result.Cells[0].Type = "invalid"
	if err := validateLocalTextCommit(context.Background(), LocalTextCommit{Capture: x, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("invalid cell kind")
	}
	if LocalTextValues(documenttext.Result{Cells: []documenttext.Cell{{Part: "sheet", Reference: "A1", Value: "raw"}}}) != "sheet\nA1\nraw" {
		t.Fatal("cell FTS provenance lost")
	}
	if err := validateLocalTextRow(context.Background(), LocalTextRow{}); err == nil {
		t.Fatal("zero row binding")
	}
}

func TestLocalImmutableRegistrationAndFailureStatuses(t *testing.T) {
	s := newStore(t)
	a := beginLocal(t, s, "docx", base)
	ctx := context.Background()
	for _, o := range []KnowledgeOutcome{{Status: "complete", Reason: "local_text_capture_failed", At: base}, {Status: "failed", Reason: "private-error", At: base}} {
		if err := s.RecordLocalTextFailure(ctx, a, o); err == nil {
			t.Fatal("bad failure")
		}
	}
	if err := s.RecordLocalTextFailure(ctx, KnowledgeAttempt{ID: 999, LocatorID: 999}, KnowledgeOutcome{Status: "failed", Reason: "local_text_capture_failed", At: base}); err == nil {
		t.Fatal("unknown failure target")
	}
	if _, err := s.BeginLocalTextAttempt(ctx, KnowledgeInput{}, officecapture.TextInput{}); err == nil {
		t.Fatal("invalid registration")
	}
	if _, err := s.db.Exec(`update local_text_locator_metadata set path='/different/file.docx'`); err != nil {
		t.Fatal(err)
	}
	input := officecapture.TextInput{Path: "/synthetic/file.docx", RequestedKind: "docx"}
	if _, err := s.BeginLocalTextAttempt(ctx, KnowledgeInput{Kind: LocalTextKind, Platform: "darwin", Locator: LocalTextLocator(input), At: base}, input); err == nil {
		t.Fatal("immutable registration overwritten")
	}
}

func TestLocalOriginalURLNeverFriendlyPath(t *testing.T) {
	registry := DocumentObservationRow{Variant: RegistryDocumentKind, Registry: &officeregistry.Observation{URL: "file:///native", FriendlyPath: "/not-authority"}}
	if OriginalDocumentURL(registry) != "file:///native" {
		t.Fatal("friendly path authorized")
	}
	if OriginalDocumentURL(DocumentObservationRow{Variant: RegistryDocumentKind}) != "" ||
		OriginalDocumentURL(DocumentObservationRow{Variant: UsageDocumentKind}) != "" {
		t.Fatal("absent native URL invented")
	}
	usage := DocumentObservationRow{Variant: UsageDocumentKind, Usage: &UsageNative{Document: &onedriveusage.DocumentObservation{URL: "file:///usage-native"}}}
	if OriginalDocumentURL(usage) != "file:///usage-native" {
		t.Fatal("usage native URL lost")
	}
}

func TestLocalSchemaAndPublicationValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.Exec(`drop table local_text_references`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LocalTextSnapshot(ctx, LocalTextFilter{}); err == nil {
		t.Fatal("incomplete schema")
	}
	s = newStore(t)
	a := beginLocal(t, s, "xlsx", base)
	c := localCapture(t, "", "")
	if err := s.CommitLocalText(ctx, a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("requested kind mismatch")
	}
	bad := c
	bad.Evidence.CreationRaw = func() *string { v := "1"; return &v }()
	if err := s.CommitLocalText(ctx, beginLocal(t, s, "docx", base), LocalTextCommit{Capture: bad, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("clock domain mismatch")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.CommitLocalText(cancelled, a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err == nil {
		t.Fatal("cancelled publication")
	}
	if err := validateLocalReference(cancelled, s.db, LocalTextReference{DocumentObservationID: 1}); err == nil {
		t.Fatal("cancelled reference validation")
	}
}

func TestLocalNativeCreationAndModifiedOrdering(t *testing.T) {
	s := newStore(t)
	c := localCapture(t, "", "A")
	c.Evidence.NativeGenerationRaw = nil
	c.Evidence.GenerationKind = "native_creation"
	commitLocal(t, s, c, base)
	c.Evidence.ModifiedAt = nil
	commitLocal(t, s, c, base.Add(time.Second))
	c = localCapture(t, "", "B")
	at := time.Unix(3, 0).UTC()
	raw := "3:0"
	c.Evidence.ModificationRaw, c.Evidence.ModifiedAt = &raw, &at
	commitLocal(t, s, c, base.Add(2*time.Second))
	input := officecapture.TextInput{Path: "/a-synthetic/file.docx", RequestedKind: "docx"}
	a, err := s.BeginLocalTextAttempt(context.Background(), KnowledgeInput{Kind: LocalTextKind, Platform: "darwin", Locator: LocalTextLocator(input), At: base}, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLocalText(context.Background(), a, LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.LocalTextSnapshot(context.Background(), LocalTextFilter{})
	if err != nil || len(snap.Rows) != 4 || snap.Rows[0].SourceID != 3 || snap.Rows[3].Snapshot.Evidence.ModifiedAt != nil {
		t.Fatalf("clock/label/native ordering: %#v %v", snap, err)
	}
}
