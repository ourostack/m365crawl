package cli

import (
	"database/sql"
	goruntime "runtime"
	"testing"
)

func TestLocalReadFailureCoverageAcrossConsumers(t *testing.T) {
	e := cloudCLIEnv(t)
	if code, _, _ := e.run("content", "show", "local-text-observation:0"); code == 0 {
		t.Fatal("invalid local observation")
	}
	if code, _, _ := e.run("content", "show", "local-text-observation:1"); code == 0 {
		t.Fatal("absent local observation")
	}
	installCloudFixture(t, nil)
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := e.run("--format", "text", "content", "show", "cloud-observation:1"); code != 0 {
		t.Fatal(stderr)
	}
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		return
	}
	path := localCLIFile(t, e)
	if code, _, stderr := e.run("content", "capture", "--file", path); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, _ := e.run("content", "show", "local-text-observation:999"); code == 0 {
		t.Fatal("unknown local observation")
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update local_text_observations set representation_digest='wrong'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"content", "show", "local-text-observation:1"}, {"content", "sources"},
		{"search", "absentword", "--source", "content"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("complete binding corruption hidden by consumer")
		}
	}
}
