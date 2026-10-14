package cli

import (
	"context"
	"database/sql"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestLocalContentMixedArchiveScopesAndFailures(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	e := cloudCLIEnv(t)
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		raw := strings.Replace(cloudFixtureRaw, `"Title":"Synthetic page"`, `"Title":"Synthetic page","ModifiedRaw":"2026-10-12T00:00:00Z"`, 1)
		return &cloudFixturePage{raw: raw, host: "fixture.sharepoint.com"}, &cloudFixtureCloser{}, nil
	}
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	path := localCLIFile(t, e)
	if code, _, stderr := e.run("content", "capture", "--file", path); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{
		{"content", "list", "--source-id", "1"}, {"content", "list", "--source-id", "2"},
		{"content", "sources", "--source-id", "1"}, {"content", "sources", "--source-id", "2"},
		{"content", "list", "--limit", "1"},
	} {
		if code, _, stderr := e.run(args...); code != 0 {
			t.Fatal(stderr)
		}
	}
	if err := os.WriteFile(path, []byte("unsupported synthetic bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("content", "capture", "--file", path); code == 0 || !strings.Contains(stderr, "local_text_unsupported") {
		t.Fatal("parser refusal hidden")
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update local_text_observations set result_json='wrong'`); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("content", "list"); code == 0 {
		t.Fatal("local corruption hidden")
	}
	if _, err := db.Exec(`update cloud_observations set result_json='wrong'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("content", "list"); code == 0 {
		t.Fatal("cloud corruption hidden")
	}
}

func TestLocalCaptureResultRequiresAdmittedMember(t *testing.T) {
	rt := &runtime{g: &Globals{}}
	if _, err := localCaptureResult(rt, store.LocalTextSnapshot{}, 1); err == nil {
		t.Fatal("missing capture success")
	}
	if _, err := localCaptureResult(rt, store.LocalTextSnapshot{Sources: []store.LocalTextSource{{Source: store.KnowledgeSource{ID: 2, AdmittedID: 1}}},
		Rows: []store.LocalTextRow{{SourceID: 2, Retained: true}, {SourceID: 3}}}, 1); err == nil {
		t.Fatal("retained or foreign member emitted as admitted")
	}
}
