package cli

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestContentReadErrorsNeverCaptureOrMigrate(t *testing.T) {
	e := cloudCLIEnv(t)
	installCloudFixture(t, nil)
	url := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	if code, _, stderr := e.run("content", "fetch", "page", url); code != 0 {
		t.Fatal(stderr)
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update cloud_observations set result_json='{}'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("content", "list"); code == 0 {
		t.Fatal("corrupt archive served")
	}
	if code, _, _ := e.run("search", "searchableword", "--source", "content"); code == 0 {
		t.Fatal("corrupt shared search served")
	}
	if code, _, _ := e.run("search", "--source", "content"); code == 0 {
		t.Fatal("unfiltered shared content search accepted")
	}
	if err := os.WriteFile(e.db, []byte("synthetic not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("content", "list"); code == 0 {
		t.Fatal("archive open failure masked")
	}
}

func TestContentBrowserResolutionFailure(t *testing.T) {
	e := cloudCLIEnv(t)
	old := findBrowser
	t.Cleanup(func() { findBrowser = old })
	findBrowser = func(string) (string, browser.Kind, error) {
		return "", browser.KindCustom, errors.New("synthetic browser resolution")
	}
	url := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	if code, _, _ := e.run("content", "fetch", "page", url); code == 0 {
		t.Fatal("browser resolution fetch")
	}
	if code, _, _ := e.run("content", "signin", url, "--user-agreed"); code == 0 {
		t.Fatal("browser resolution signin")
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("browser failure opened archive")
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
