package cli

import (
	"os"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestLocalRetainedPartialOwnSnapshotAndCurrentCoverage(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	path := localCLIFile(t, e, `<w:p><w:r><w:t>partialword</w:t><w:object/></w:r></w:p>`)
	if code, out, stderr := e.run("content", "capture", "--file", path); code != 0 || !strings.Contains(out, "embedded_content_unmapped") {
		t.Fatalf("production partial fixture: %d %s %s", code, out, stderr)
	}
	cfb := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	if err := os.WriteFile(path, cfb, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := e.run("content", "capture", "--file", path); code != 0 || !strings.Contains(out, "compound_unreadable") || items(t, decode(t, out))[0]["verified_kind"] != nil {
		t.Fatalf("compound intent: %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{"content", "show", "local-text-observation:1"},
		{"--max-text", "2", "--fields", "id,partial,losses,snapshot,current_snapshot", "content", "list"},
		{"--fields", "id", "search", "absentword", "--source", "content", "--limit", "1"},
		{"--fields", "id", "search", "partialword", "--source", "content", "--limit", "1"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		if !strings.Contains(out, "local_content_coverage") || !strings.Contains(out, "compound_unreadable") {
			t.Fatal("current coverage lost behind retained rows/fields/zero hits")
		}
		if args[0] == "content" {
			row := items(t, decode(t, out))[0]
			if row["partial"] != true || row["retained"] != true || row["snapshot_match"] != "differs" {
				t.Fatal("retained own qualification replaced")
			}
			snapshot := row["snapshot"].(map[string]any)
			current := row["current_snapshot"].(map[string]any)
			if snapshot["SourceByteDigest"] == current["SourceByteDigest"] {
				t.Fatal("own/current snapshot mixed")
			}
			loss := row["losses"].([]any)[0].(map[string]any)
			if loss["Code"] != "embedded_content_unmapped" || loss["Count"] != float64(1) {
				t.Fatal("own counted losses replaced by B")
			}
			if code, full, stderr := e.run("sql", row["full_text_sql"].(string)); code != 0 || !strings.Contains(full, "partialword") {
				t.Fatalf("full own record SQL: %d %s", code, stderr)
			}
		}
	}
}
