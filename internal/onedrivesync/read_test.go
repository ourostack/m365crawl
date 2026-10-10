package onedrivesync

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const nativeSchema = `
CREATE TABLE od_ScopeInfo_Records(scopeID TEXT PRIMARY KEY,sourceResourceID TEXT,siteID TEXT,webID TEXT,listID TEXT,webURL TEXT,remotePath TEXT,lastKnownFolderPath TEXT);
CREATE TABLE od_ClientFile_Records(resourceID TEXT PRIMARY KEY,parentResourceID TEXT,fileName TEXT,size INTEGER,lastChange INTEGER,serverLastChange INTEGER,fileStatus INTEGER,lastKnownPinState INTEGER);
CREATE TABLE od_ClientFolder_Records(resourceID TEXT PRIMARY KEY,parentResourceID TEXT,parentScopeID TEXT,folderName TEXT);
CREATE TABLE od_GraphMetadata_Records(resourceID TEXT PRIMARY KEY,createdBy TEXT,modifiedBy TEXT,spoCompositeID TEXT);
CREATE TABLE od_ClientPolicy_Records(siteID TEXT,webID TEXT,listID TEXT,graphDriveId TEXT,siteTitle TEXT,libraryTitle TEXT,viewOnlineUrlTemplate TEXT,shareUrlTemplate TEXT,wacUrlTemplate TEXT,davUrlTemplate TEXT,PRIMARY KEY(siteID,webID,listID));
CREATE TABLE od_HydrationData(resourceID TEXT PRIMARY KEY,firstHydrationTime INTEGER,lastHydrationTime INTEGER,hydrationCount INTEGER,lastHydrationType TEXT);
`

func fixture(t *testing.T, schema, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sync.db")
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(schema + rows); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const nativeRows = `
INSERT INTO od_ScopeInfo_Records VALUES('scope','source','site','web','list','https://example.invalid','raw/path','raw/root');
INSERT INTO od_ClientFolder_Records VALUES('folder','scope','contradictory-hint','Folder');
INSERT INTO od_ClientFile_Records VALUES('file','folder','Name',NULL,123,NULL,99,NULL);
INSERT INTO od_GraphMetadata_Records VALUES('file','Creator','Modifier','opaque-composite');
INSERT INTO od_ClientPolicy_Records VALUES('site','web','list','drive','Site','Library','view-{id}','share-{id}','web-{id}','dav-{path}');
INSERT INTO od_HydrationData VALUES('file',NULL,88,2,'unknown-native-type');
`

func TestReadIndexRawEvidence(t *testing.T) {
	got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, nativeRows))
	if err != nil || len(got.Scopes) != 1 || len(got.Files) != 1 || len(got.Folders) != 1 ||
		len(got.Graph) != 1 || len(got.Policies) != 1 || len(got.Hydration) != 1 || len(got.Losses) != 0 {
		t.Fatalf("native capture missing: %#v,%v", got, err)
	}
	file := got.Files[0]
	if file.ID != "file" || file.ParentID != "folder" || file.Name != "Name" ||
		file.SizeRaw != nil || file.ServerChangedRaw != nil || file.PinRaw != nil ||
		file.ChangedRaw == nil || *file.ChangedRaw != 123 || file.StatusRaw == nil || *file.StatusRaw != 99 {
		t.Fatalf("raw nullable state coerced: %#v", file)
	}
	if got.Folders[0].ParentScopeID != "contradictory-hint" || got.Graph[0].CreatedBy != "Creator" ||
		got.Policies[0].ViewURLTemplate != "view-{id}" || got.Hydration[0].FirstRaw != nil ||
		got.Hydration[0].TypeRaw != "unknown-native-type" || got.Scopes[0].LastKnownFolderPath != "raw/root" {
		t.Fatalf("native observations interpreted/lost: %#v", got)
	}
	second, err := ReadIndex(context.Background(), fixture(t, nativeSchema, nativeRows))
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatalf("same native IDs in another supplied source merged: %#v,%v", second, err)
	}
}

func TestReadIndexFileFolderIdentityOverlapRefusesAll(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows+
		`INSERT INTO od_ClientFolder_Records VALUES('file','scope','scope','Collision');`)
	got, err := ReadIndex(context.Background(), path)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_ambiguous" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("file/folder identity collision accepted: %#v,%v", got, err)
	}
}

func TestReadIndexMissingIdentityIsRowLoss(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows+
		`INSERT INTO od_GraphMetadata_Records VALUES(NULL,'No identity',NULL,NULL);`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Graph) != 1 || !reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_sync_graph_unmapped", Count: 1}}) {
		t.Fatalf("missing native identity admitted: %#v,%v", got, err)
	}
}

func TestReadIndexParentEvidence(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows+`
INSERT INTO od_ClientFolder_Records VALUES
('a','b','ignored','A'),('b','a','ignored','B'),('c','a','ignored','Tail'),
('d','missing','scope','Missing'),('e','d','scope','Missing-tail'),('empty','','scope','Empty');
INSERT INTO od_ClientFile_Records VALUES
('cycle-tail','c','Cycle-tail',NULL,NULL,NULL,NULL,NULL),
('missing-tail','e','Missing-tail',NULL,NULL,NULL,NULL,NULL),
('wrong-kind','file','Wrong-kind',NULL,NULL,NULL,NULL,NULL);
`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{{Code: "onedrive_sync_parent_cycle", Count: 2}, {Code: "onedrive_sync_parent_unresolved", Count: 7}}
	if err != nil || len(got.Folders) != 7 || len(got.Files) != 4 || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("native ancestry outcomes lost: %v,%v", got.Losses, err)
	}
}

func TestReadIndexAmbiguousParentRetainsEvidence(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows+
		`INSERT INTO od_ClientFolder_Records VALUES('scope','scope','scope','Ambiguous');`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{{Code: "onedrive_sync_parent_unresolved", Count: 3}}
	if err != nil || len(got.Folders) != 2 || len(got.Files) != 1 || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("ambiguous parent chose authority: %v,%v", got.Losses, err)
	}
}

func TestReadIndexFilteredTargetsDoNotResolveReferences(t *testing.T) {
	for _, test := range []struct {
		old, replacement string
		want             []Loss
	}{
		{"'Name'", "x'ff'", []Loss{
			{Code: "onedrive_sync_file_unmapped", Count: 1},
			{Code: "onedrive_sync_graph_orphan", Count: 1},
			{Code: "onedrive_sync_hydration_orphan", Count: 1},
		}},
		{"'Folder'", "x'ff'", []Loss{
			{Code: "onedrive_sync_folder_unmapped", Count: 1},
			{Code: "onedrive_sync_parent_unresolved", Count: 1},
		}},
		{"'Library'", "x'ff'", []Loss{
			{Code: "onedrive_sync_policy_unmapped", Count: 1},
			{Code: "onedrive_sync_scope_policy_unknown", Count: 1},
		}},
	} {
		got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, strings.Replace(nativeRows, test.old, test.replacement, 1)))
		if err != nil || !reflect.DeepEqual(got.Losses, test.want) {
			t.Fatalf("filtered target used seen-ID as authority: %v,%v", got.Losses, err)
		}
	}
}

func TestReadIndexEmpty(t *testing.T) {
	got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, ""))
	if err != nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("empty source: %#v,%v", got, err)
	}
}
