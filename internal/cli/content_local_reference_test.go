package cli

import (
	"context"
	"net/url"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func seedLocalReferenceCLI(t *testing.T, e *env, native string) {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	a, err := st.BeginDocumentAttempt(context.Background(), store.KnowledgeInput{Kind: "office_shared", Platform: "synthetic", Locator: "/fixture/reference.json", At: at}, officedocuments.Shared, "supplied")
	if err != nil {
		t.Fatal(err)
	}
	result := officedocuments.Result{Documents: []officedocuments.Document{{Ordinal: 0, Surface: officedocuments.Shared, Title: "Synthetic selected reference", URL: native}}}
	if err := st.CommitDocumentObservations(context.Background(), a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "synthetic-reference", Qualification: store.DocumentQualification, At: at}, result,
		store.DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReferenceExplicitNativeSelection(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	file := localCLIFile(t, e)
	path := filepath.ToSlash(file)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	native := (&url.URL{Scheme: "file", Path: path}).String()
	seedLocalReferenceCLI(t, e, native)
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	input, ref, err := localTextInput(context.Background(), st, contentCaptureCmd{From: "document-observation:1"})
	if err != nil || input.Path != file || ref == nil || ref.OriginalURL != native {
		t.Fatalf("selected original locator: %#v %#v %v", input, ref, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := e.run("content", "capture", "--from", "document-observation:1"); code != 0 || !strings.Contains(out, "local-text-observation:") {
		t.Fatalf("reference capture: %d %s %s", code, out, stderr)
	}
	code, out, stderr := e.run("content", "show", "local-text-observation:1")
	if code != 0 || !strings.Contains(out, "reference_locator_match") || !strings.Contains(out, "DocumentObservationID") {
		t.Fatalf("atomic origin lost: %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{"content", "list", "--source-id", "2"}, {"content", "sources", "--source-id", "2"},
		{"content", "search", "residentword", "--source-id", "2"}, {"content", "list", "--source-id", "999"},
	} {
		code, _, stderr := e.run(args...)
		if strings.Contains(strings.Join(args, " "), "999") {
			if code == 0 {
				t.Fatal("unknown source")
			}
		} else if code != 0 {
			t.Fatal(stderr)
		}
	}
	if localSourceFor(nil, 99).RegistrationID != 0 || LocalPreviewText(LocalTextItem{Cells: []documenttext.Cell{{Value: "raw"}}}) != "raw" {
		t.Fatal("local source or cell preview")
	}
}

func TestLocalReferenceRefusalsAreArchiveOnly(t *testing.T) {
	e := cloudCLIEnv(t)
	if code, _, _ := e.run("content", "capture", "--from", "document-observation:1"); code == 0 {
		t.Fatal("absent archive selected")
	}
	for _, cmd := range []contentCaptureCmd{{}, {File: "a", From: "document-observation:1"}, {From: "wrong"}, {From: "document-observation:1"}} {
		if _, _, err := localTextInput(context.Background(), nil, cmd); err == nil {
			t.Fatal("invalid selection")
		}
	}
	seedLocalReferenceCLI(t, e, "https://fixture.sharepoint.com/sites/sample/file.docx")
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, id := range []string{"document-observation:1", "document-observation:999"} {
		if _, _, err := localTextInput(context.Background(), st, contentCaptureCmd{From: id}); err == nil {
			t.Fatal("cloud/missing selected original opened")
		}
	}
	if code, _, stderr := e.run("content", "capture", "--from", "document-observation:1"); code == 0 || !strings.Contains(stderr, "--file") {
		t.Fatal("unsupported cloud URL has no explicit local route")
	}
	if code, _, _ := e.run("--account", "synthetic/user", "content", "capture", "--file", "/synthetic/file.docx"); code == 0 {
		t.Fatal("unresolved account filter")
	}
	if code, _, _ := e.run("--fields", "bad", "content", "capture", "--file", "/synthetic/file.docx"); code == 0 {
		t.Fatal("invalid fields")
	}
	for _, raw := range []string{"file:///a", "file:///C:/a:stream.docx", "file:///C:/a/../b.docx"} {
		if _, err := localFileURL(raw, "windows"); err == nil {
			t.Fatal("unsafe Windows file URL")
		}
	}
	if path, err := localFileURL("file:///C:/a%20b.docx", "windows"); err != nil || path != `C:\a b.docx` {
		t.Fatalf("portable Windows parse: %s %v", path, err)
	}
}
