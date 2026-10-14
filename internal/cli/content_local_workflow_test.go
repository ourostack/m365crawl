package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"context"
	"github.com/ourostack/m365crawl/internal/store"
)

func localCLIFile(t *testing.T, e *env, bodies ...string) string {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	body := `<w:p><w:r><w:t>residentword long text</w:t></w:r></w:p>`
	if len(bodies) > 0 {
		body = bodies[0]
	}
	for _, p := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`},
	} {
		member, err := w.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(e.db))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "synthetic.docx")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalContentCaptureThenOfflineReads(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native capture only Darwin/Windows")
	}
	e := cloudCLIEnv(t)
	path := localCLIFile(t, e)
	code, out, stderr := e.run("content", "capture", "--file", path)
	if code != 0 || !strings.Contains(out, "local-text-observation:") || !strings.Contains(out, "residentword") {
		t.Fatalf("explicit native synthetic: code=%d out=%s stderr=%s", code, out, stderr)
	}
	// Renaming the selected source proves ordinary reads neither open nor retry it.
	if err := os.Rename(path, path+".offline"); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("content", "capture", "--file", path+".offline", "--kind", "docx"); code != 0 {
		t.Fatal(stderr)
	}
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"content", "list"}, {"content", "search", "residentword"}, {"content", "show", "local-text-observation:1"}, {"content", "sources"},
		{"--max-age", "1ns", "search", "residentword", "--source", "content"},
		{"--fields", "id", "search", "absentword", "--source", "content"},
		{"--format", "text", "content", "list"}, {"--format", "text", "content", "show", "local-text-observation:1"},
		{"--format", "text", "search", "residentword", "--source", "content"}, {"--format", "text", "content", "sources"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatalf("offline %v: %s", args, stderr)
		}
		if strings.Join(args, " ") == "--format text content show local-text-observation:1" {
			for _, want := range []string{"source-byte digest", "representation digest", "native_filesystem_modified", "full archive SQL", "local content:", "occurrences"} {
				if !strings.Contains(out, want) {
					t.Fatalf("CLI detail missing %s", want)
				}
			}
		}
		if !strings.Contains(out, "local_content_coverage") && !strings.Contains(strings.Join(args, " "), "--format text") {
			t.Fatal("coverage lost")
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("ordinary reads wrote archive")
	}
	if code, _, stderr := e.run("content", "capture", "--file", path); code == 0 || !strings.Contains(stderr, "registration_id=1") {
		t.Fatalf("failed retry has no registration: %d %s", code, stderr)
	}
	if code, out, stderr := e.run("content", "sources"); code != 0 || !strings.Contains(out, "local_text_input_unqualified") || !strings.Contains(out, "--file") {
		t.Fatalf("failed coverage: %d %s %s", code, out, stderr)
	}
}
