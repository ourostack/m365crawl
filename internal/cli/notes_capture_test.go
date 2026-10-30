package cli

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func notesCLIIndex(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "index.db")
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
create table Entities(rowid integer primary key,Type integer,GOID text,GUID text,GOSID text,ParentGOID text,GrandparentGOIDs text,LastModifiedTime integer,Title text);
create table PageElements(rowid integer primary key,GOID text,Text text,Jcid integer,EntityRowId integer);
create table NoteFlags(rowid integer primary key,Type integer,Shape integer,Status integer,Label text,PageElementRowId integer);
create table Hashtags(rowid integer primary key,PageElementRowId integer);
insert into Entities values(4,4,'notebook','notebook-guid',null,null,null,null,'Synthetic notebook'),(1,1,'page','page-guid',null,'notebook',null,116444736000000000,'Captured fixture');
insert into PageElements values(8,'text','Captured searchableword',393230,1);
insert into NoteFlags values(9,1,2,3,'Priority',8);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNotesCLIExplicitSyncIsAConnectedOfflineWorkflow(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	notesCLIIndex(t, root)
	for _, format := range []string{"json", "text"} {
		code, stdout, stderr := e.run("--format", format, "notes", "sync", "--root", root)
		if code != 0 || !strings.Contains(stdout, "unchanged") {
			t.Fatalf("explicit capture: %d/%s/%s", code, stdout, stderr)
		}
	}
	code, stdout, stderr := e.run("--no-sync", "notes", "search", "searchableword")
	if code != 0 {
		t.Fatal(stderr)
	}
	rows := items(t, decode(t, stdout))
	if len(rows) != 1 {
		t.Fatal("capture was not connected to search")
	}
	code, stdout, stderr = e.run("--no-sync", "notes", "show", rows[0]["id"].(string))
	if code != 0 || len(items(t, decode(t, stdout))[0]["flags"].([]any)) != 1 {
		t.Fatalf("captured native flags missing: %d/%s/%s", code, stdout, stderr)
	}
	code, stdout, stderr = e.run("--no-sync", "search", "searchableword", "--source", "notes", "--fields", "source,id", "--max-text", "5")
	if code != 0 || decode(t, stdout)["source_coverage"] == nil {
		t.Fatalf("shared focused recall: %d/%s/%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "index.db-wal")); !os.IsNotExist(err) {
		t.Fatal("capture wrote a source WAL companion")
	}
}

func TestNotesCLIValidationIsBeforeCapture(t *testing.T) {
	e := newEnv(t)
	seedNotesCLI(t, e)
	for _, args := range [][]string{
		{"notes", "sync", "--root", "/missing"},
		{"--account", "tenant/user", "notes", "sync"},
		{"--fields", "bad", "notes", "list"}, {"--fields", "bad", "notes", "search", "x"},
		{"--fields", "bad", "notes", "show", "note:2"}, {"--fields", "bad", "notes", "sources"},
		{"notes", "list", "--since", "invalid"}, {"notes", "list", "--until", "invalid"},
		{"notes", "list", "--source", "999"},
		{"notes", "list", "--source=-1"},
		{"search", "--source", "notes"},
	} {
		if code, _, stderr := e.run(append([]string{"--no-sync"}, args...)...); code == 0 {
			t.Fatalf("invalid capture/read accepted %v: %s", args, stderr)
		}
	}
}
