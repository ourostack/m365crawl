package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/sourcecapture"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestLocalIncarnationDetailExplainsInstanceSnapshotMatch(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	path := localCLIFile(t, e)
	if code, _, stderr := e.run("content", "capture", "--file", path); code != 0 {
		t.Fatal(stderr)
	}
	if err := os.Rename(path, path+".incarnation-a"); err != nil {
		t.Fatal(err)
	}
	localCLIFile(t, e)
	if code, _, stderr := e.run("content", "capture", "--file", path); code != 0 {
		t.Fatal(stderr)
	}
	code, out, stderr := e.run("content", "show", "local-text-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	row := items(t, decode(t, out))[0]
	if row["retained"] != true || row["snapshot_match"] != "matches" ||
		row["snapshot"].(map[string]any)["ID"] != row["current_snapshot"].(map[string]any)["ID"] {
		t.Fatal("per-incarnation snapshot semantics changed")
	}
	if row["snapshot_match_scope"] != "current_archived_acquisition_for_this_source_instance" {
		t.Fatal("CURRENT match scope is ambiguous")
	}
	if code, out, stderr := e.run("--format", "text", "content", "show", "local-text-observation:1"); code != 0 ||
		!strings.Contains(out, "for THIS source instance") || !strings.Contains(out, "retained=true") {
		t.Fatalf("retained matching instance not explained: %s %s", out, stderr)
	}
}

func TestLocalNativeLongPartCaptureAndOfflineFullRecord(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	path := localCLIFile(t, e)
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
	if result, err := documenttext.Extract(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()), "docx", documenttext.DefaultLimits()); err != nil ||
		len(result.Paragraphs) != 1 || result.Paragraphs[0].Part != part {
		t.Fatal("producer long-part fixture")
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("content", "capture", "--file", path); code != 0 {
		t.Fatal(stderr)
	}
	if err := os.Rename(path, path+".offline"); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("content", "show", "local-text-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	row := items(t, decode(t, out))[0]
	if row["paragraphs"].([]any)[0].(map[string]any)["Part"] != part {
		t.Fatal("native long part clipped/changed")
	}
	if code, full, stderr := e.run("sql", row["full_text_sql"].(string)); code != 0 || !strings.Contains(full, part) {
		t.Fatalf("full record lost long part: %d %s", code, stderr)
	}
	if code, _, stderr := e.run("content", "search", "longpartword"); code != 0 {
		t.Fatal(stderr)
	}
}

func TestLocalYearOneSentinelOfflineCLI(t *testing.T) {
	e := cloudCLIEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	input := officecapture.TextInput{Path: "/synthetic/sentinel.docx", RequestedKind: "docx"}
	// Archived Darwin evidence is portable even on a Windows reader.
	at := time.Now().UTC()
	a, err := st.BeginLocalTextAttempt(context.Background(), store.KnowledgeInput{Kind: store.LocalTextKind, Platform: "darwin", Locator: store.LocalTextLocator(input), At: at}, input)
	if err != nil {
		t.Fatal(err)
	}
	raw, gen, creation := "-62135596800:0", "7", "1:0"
	kind := "docx"
	c := store.LocalTextCapture{RequestedKind: kind, VerifiedKind: &kind, SourceByteDigest: strings.Repeat("a", 64), AcquiredAt: at,
		Result: documenttext.Result{Kind: kind, State: "text_observations"},
		Evidence: officecapture.FileEvidence{VolumeID: "1", FileID: "2", GenerationKind: "native_generation", GenerationPolicy: officecapture.FileGenerationPolicy,
			NativeGenerationRaw: &gen, CreationRaw: &creation, ModificationRaw: &raw, GenerationQualified: true, Size: 8, NativeAttributes: "0"}}
	if err := st.CommitLocalText(context.Background(), a, store.LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"content", "list"}, {"content", "search", "absentword"}, {"content", "show", "local-text-observation:1"}, {"content", "sources"}} {
		code, out, stderr := e.run(args...)
		if code != 0 || !strings.Contains(out, raw) || strings.Contains(out, "0001-01-01") {
			t.Fatalf("sentinel CLI raw/null: %v %s", args, stderr)
		}
	}

}

func TestLocalCancellationAfterAttemptRetainsIndependentDiagnostic(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	path := localCLIFile(t, e)
	release, err := sourcecapture.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rt := &runtime{ctx: ctx, g: &Globals{}, dbPath: e.db, stdout: io.Discard}
	if err := rt.captureLocalText(officecapture.TextInput{Path: path, RequestedKind: "docx"}, "explicit", nil); err == nil {
		t.Fatal("cancelled acquisition succeeded")
	}
	code, out, stderr := e.run("content", "sources")
	if code != 0 || !strings.Contains(out, "local_text_cancelled") || !strings.Contains(out, `"cancelled"`) {
		t.Fatalf("cancelled attempt diagnostic missing: %s %s", out, stderr)
	}
}
