package onenote

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const fixtureSchema = `
CREATE TABLE Entities(rowid INTEGER PRIMARY KEY, Type INTEGER, GOID TEXT NOT NULL,
 GUID TEXT NOT NULL, GOSID TEXT, ParentGOID TEXT, GrandparentGOIDs TEXT,
 LastModifiedTime INTEGER, Title TEXT);
CREATE TABLE PageElements(rowid INTEGER PRIMARY KEY, GOID TEXT NOT NULL,
 Text TEXT, Jcid INTEGER, EntityRowId INTEGER);
CREATE TABLE NoteFlags(rowid INTEGER PRIMARY KEY, Type INTEGER NOT NULL,
 Shape INTEGER NOT NULL, Status INTEGER NOT NULL, Label TEXT NOT NULL,
 PageElementRowId INTEGER NOT NULL);
CREATE TABLE Hashtags(rowid INTEGER PRIMARY KEY, PageElementRowId INTEGER);
`

func indexFixture(t *testing.T, name string, statements ...string) string {
	t.Helper()
	return indexFixtureWithSchema(t, name, fixtureSchema, statements...)
}

func indexFixtureWithSchema(t *testing.T, name, schema string, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const hierarchyFixture = `
INSERT INTO Entities VALUES
(40,4,'notebook','guid-notebook','sid-notebook',NULL,NULL,116444736000000000,'Notebook'),
(30,2,'section','guid-section','sid-section','group','notebook',NULL,'Section'),
(20,1,'page','guid-page','sid-page','section','group;notebook',116444736010000007,'A fictional page'),
(12,3,'group','guid-group',NULL,'notebook',NULL,0,NULL);
INSERT INTO PageElements VALUES
(91,'ocr','Picture words',393233,20),
(81,'paragraph','Hello 世界',393230,20);
INSERT INTO NoteFlags VALUES (9,7,3,11,'Priority',81);
INSERT INTO Hashtags VALUES (4,81),(5,81);
`

func TestReadIndexHierarchyTextAndFlags(t *testing.T) {
	got, err := ReadIndex(context.Background(), indexFixture(t, "index.db", hierarchyFixture))
	if err != nil {
		t.Fatal(err)
	}
	wantEntities := []Entity{
		{RowID: 12, Type: 3, GOID: "group", GUID: "guid-group", ParentGOID: "notebook"},
		{RowID: 20, Type: 1, GOID: "page", GUID: "guid-page", GOSID: "sid-page",
			ParentGOID: "section", GrandparentGOIDs: "group;notebook", Title: "A fictional page",
			ModifiedAt: time.Date(1970, 1, 1, 0, 0, 1, 700, time.UTC)},
		{RowID: 30, Type: 2, GOID: "section", GUID: "guid-section", GOSID: "sid-section",
			ParentGOID: "group", GrandparentGOIDs: "notebook", Title: "Section"},
		{RowID: 40, Type: 4, GOID: "notebook", GUID: "guid-notebook", GOSID: "sid-notebook",
			Title: "Notebook", ModifiedAt: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	wantElements := []Element{
		{RowID: 81, EntityRowID: 20, Jcid: 393230, GOID: "paragraph", Text: "Hello 世界",
			Kind: "rich_text", HashtagMarkers: 2},
		{RowID: 91, EntityRowID: 20, Jcid: 393233, GOID: "ocr", Text: "Picture words", Kind: "ocr"},
	}
	wantFlags := []Flag{{RowID: 9, ElementRowID: 81, Type: 7, Shape: 3, Status: 11, Label: "Priority"}}
	if !reflect.DeepEqual(got.Entities, wantEntities) || !reflect.DeepEqual(got.Elements, wantElements) ||
		!reflect.DeepEqual(got.Flags, wantFlags) || got.Ordering != "index_rowid_not_document_layout" {
		t.Fatalf("observations = %#v", got)
	}
	if !reflect.DeepEqual(got.Losses, []Loss{{Code: "onenote_modified_unknown", Count: 1}}) {
		t.Fatalf("nullable/zero clock losses = %#v", got.Losses)
	}
}

func TestReadIndexEmptyAndNullable(t *testing.T) {
	empty, err := ReadIndex(context.Background(), indexFixture(t, "empty.db"))
	if err != nil || len(empty.Entities) != 0 || len(empty.Elements) != 0 ||
		len(empty.Flags) != 0 || len(empty.Losses) != 0 || empty.Ordering != "index_rowid_not_document_layout" {
		t.Fatalf("empty = %#v, error %v", empty, err)
	}
	path := indexFixture(t, "nullable.db", `
INSERT INTO Entities VALUES(1,1,'page','guid',NULL,NULL,NULL,NULL,NULL);
INSERT INTO PageElements VALUES(2,'element',NULL,393230,1);`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Entities) != 1 || len(got.Elements) != 1 ||
		got.Entities[0].Title != "" || got.Elements[0].Text != "" || len(got.Losses) != 0 {
		t.Fatalf("nullable = %#v, error %v", got, err)
	}
}

func TestReadIndexCountsOrphanAndNonPageElements(t *testing.T) {
	path := indexFixture(t, "references.db", `
INSERT INTO Entities VALUES(1,2,'section','guid',NULL,NULL,NULL,NULL,NULL);
INSERT INTO PageElements VALUES
(2,'non-page','must not become page text',393230,1),
(3,'orphan','unrelated',393230,99);
INSERT INTO NoteFlags VALUES(4,1,1,1,'Unknown',2);
INSERT INTO Hashtags VALUES(5,3);`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{
		{Code: "onenote_element_orphan", Count: 2},
		{Code: "onenote_flag_orphan", Count: 1},
		{Code: "onenote_hashtag_orphan", Count: 1},
	}
	if err != nil || len(got.Entities) != 1 || len(got.Elements) != 0 || len(got.Flags) != 0 ||
		!reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("orphan/non-page = %#v, error %v", got, err)
	}
}

func TestReadIndexRejectsMalformedStorageTypes(t *testing.T) {
	path := indexFixture(t, "types.db", `
INSERT INTO Entities VALUES
(1,1,'valid-page','valid-guid',NULL,NULL,NULL,NULL,NULL),
(2,1,x'70616765','blob-guid',NULL,NULL,NULL,NULL,NULL),
(3,'not-an-integer','bad-kind','kind-guid',NULL,NULL,NULL,NULL,NULL),
(4,1,'','empty-guid',NULL,NULL,NULL,NULL,NULL);
INSERT INTO PageElements VALUES
(5,'valid-element','Good',393230,1),
(6,'bad-text',x'666f7262696464656e',393230,1),
(7,'bad-jcid','Wrong kind type','unknown-kind',1);
INSERT INTO NoteFlags VALUES(8,'bad-type',1,1,'Flag',5);`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{
		{Code: "onenote_element_unmapped", Count: 2},
		{Code: "onenote_entity_unmapped", Count: 3},
		{Code: "onenote_flag_unmapped", Count: 1},
	}
	if err != nil || len(got.Entities) != 1 || len(got.Elements) != 1 || len(got.Flags) != 0 ||
		!reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("storage types = %#v, error %v", got, err)
	}
}

func TestReadIndexUnknownKindsHaveExplicitLosses(t *testing.T) {
	path := indexFixture(t, "kinds.db", `
INSERT INTO Entities VALUES
(1,1,'page','guid',NULL,NULL,NULL,NULL,NULL),
(2,7,'unknown','unknown-guid',NULL,NULL,NULL,NULL,NULL);
INSERT INTO PageElements VALUES(3,'element','Not a supported projection',77,1);`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{
		{Code: "onenote_element_unsupported", Count: 1},
		{Code: "onenote_entity_unsupported", Count: 1},
	}
	if err != nil || len(got.Entities) != 1 || len(got.Elements) != 0 ||
		!reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("unknown kinds = %#v, error %v", got, err)
	}
}

func TestReadIndexUnsupportedLayoutDiscardsAll(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE Hashtags RENAME COLUMN PageElementRowId TO ChangedReference",
		"DROP TABLE Hashtags; CREATE VIEW Hashtags AS SELECT rowid, PageElementRowId FROM NoteFlags",
		"INSERT INTO Entities VALUES(50,1,'page','another-guid',NULL,NULL,NULL,NULL,'Duplicate')",
	} {
		path := indexFixture(t, "layout.db", hierarchyFixture, statement)
		got, err := ReadIndex(context.Background(), path)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported layout = %#v, error %v", got, err)
		}
	}
}

func TestReadIndexEscapedReadOnlyPath(t *testing.T) {
	name := "index # space %.db"
	if runtime.GOOS != "windows" {
		name = "index # space % ?.db"
	}
	path := indexFixture(t, name, hierarchyFixture,
		"ALTER TABLE Entities ADD COLUMN Unconsumed TEXT")
	before, err := os.ReadFile(path) // #nosec G304 -- path is a synthetic fixture in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Entities) != 4 {
		t.Fatalf("escaped private copy = %#v, error %v", got, err)
	}
	after, err := os.ReadFile(path) // #nosec G304 -- path is a synthetic fixture in t.TempDir.
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed source bytes")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("read created sidecar %s", suffix)
		}
	}
}

func TestReadIndexRetentionBoundsRefuseAll(t *testing.T) {
	path := indexFixture(t, "limits.db", hierarchyFixture)
	base := readLimits{
		fileBytes: 1 << 20, stringBytes: 1 << 20, fieldBytes: 1 << 20,
		entities: 100, elements: 100, flags: 100, hashtags: 100,
	}
	for _, name := range []string{"file", "strings", "field", "entities", "elements", "flags", "hashtags"} {
		t.Run(name, func(t *testing.T) {
			limits := base
			switch name {
			case "file":
				limits.fileBytes = 1
			case "strings":
				limits.stringBytes = 1
			case "field":
				limits.fieldBytes = 1
			case "entities":
				limits.entities = 1
			case "elements":
				limits.elements = 1
			case "flags":
				limits.flags = 0
			case "hashtags":
				limits.hashtags = 1
			}
			got, err := readIndex(context.Background(), path, limits)
			if err == nil || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("limit %s = %#v, error %v", name, got, err)
			}
		})
	}
}

func TestReadIndexCancelledAndInvalidSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := ReadIndex(ctx, indexFixture(t, "cancel.db", hierarchyFixture))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled = %#v, error %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "private-source-value.db")
	if err := os.WriteFile(path, []byte("not a SQLite index"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{path, path + "-missing", filepath.Dir(path)} {
		got, err := ReadIndex(context.Background(), source)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-value") {
			t.Fatalf("unsafe invalid-source result %#v, error %v", got, err)
		}
	}
}

func TestReadIndexLateStringRetentionLimits(t *testing.T) {
	path := indexFixture(t, "late-strings.db", `
INSERT INTO Entities VALUES(1,1,'p','g',NULL,NULL,NULL,NULL,NULL);
INSERT INTO PageElements VALUES(2,'e','text',393230,1);
INSERT INTO NoteFlags VALUES(3,1,2,3,'flag',2);`)
	for _, limit := range []int64{1, 2, 7, 10} {
		got, err := readIndex(context.Background(), path, readLimits{
			fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: limit,
			entities: 1, elements: 1, flags: 1, hashtags: 0,
		})
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("string payload bound %d retained output: %#v, %v", limit, got, err)
		}
	}
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 6, stringBytes: 11,
		entities: 1, elements: 1, flags: 1, hashtags: 0,
	})
	if err != nil || len(got.Entities) != 1 || len(got.Elements) != 1 || len(got.Flags) != 1 {
		t.Fatalf("inclusive payload limits refused valid index: %#v, %v", got, err)
	}
}

func TestReadIndexMalformedMarkersAndModifiedTimes(t *testing.T) {
	path := indexFixture(t, "malformed-markers.db", `
INSERT INTO Entities VALUES
(1,1,'page','guid',NULL,NULL,NULL,'invalid-time',NULL),
(2,1,'negative','negative-guid',NULL,NULL,NULL,-1,NULL),
(3,1,'nul-guid','bad'||char(0),NULL,NULL,NULL,NULL,NULL),
(4,1,'bad-utf8',CAST(x'ff' AS TEXT),NULL,NULL,NULL,NULL,NULL);
INSERT INTO Hashtags VALUES(5,'not-an-integer');`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{
		{Code: "onenote_entity_unmapped", Count: 2},
		{Code: "onenote_hashtag_unmapped", Count: 1},
		{Code: "onenote_modified_unknown", Count: 2},
	}
	if err != nil || len(got.Entities) != 2 || !reflect.DeepEqual(got.Losses, want) ||
		!got.Entities[0].ModifiedAt.IsZero() || !got.Entities[1].ModifiedAt.IsZero() {
		t.Fatalf("malformed marker/clock admission: %#v, %v", got, err)
	}
}

func TestReadIndexDuplicateGOIDCannotHideInRefusedEntity(t *testing.T) {
	for _, statement := range []string{
		"INSERT INTO Entities VALUES(51,9,'page','guid-unknown',NULL,NULL,NULL,NULL,NULL)",
		"INSERT INTO Entities VALUES(52,1,'page',x'ff',NULL,NULL,NULL,NULL,NULL)",
	} {
		path := indexFixture(t, "duplicate-refused.db", hierarchyFixture, statement)
		got, err := ReadIndex(context.Background(), path)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("ambiguous native page identity published: %#v, %v", got, err)
		}
	}
}

func TestReadIndexUTF16CannotBypassFieldBounds(t *testing.T) {
	path := indexFixtureWithSchema(t, "utf16.db", "PRAGMA encoding='UTF-16le';"+fixtureSchema, `
INSERT INTO Entities VALUES(1,1,'p','g',NULL,NULL,NULL,NULL,'界界界界');`)
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 10, stringBytes: 100,
		entities: 1, elements: 0, flags: 0, hashtags: 0,
	})
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("UTF-16 encoding bypass published oversized UTF-8: %#v, %v", got, err)
	}
}

func TestReadIndexUTF8FieldBoundaryInclusive(t *testing.T) {
	path := indexFixture(t, "utf8.db", `
INSERT INTO Entities VALUES(1,1,'p','g',NULL,NULL,NULL,NULL,'界界界界');`)
	for _, limit := range []int64{11, 12} {
		got, err := readIndex(context.Background(), path, readLimits{
			fileBytes: 1 << 20, fieldBytes: limit, stringBytes: 14,
			entities: 1, elements: 0, flags: 0, hashtags: 0,
		})
		if limit == 11 {
			if err == nil || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("UTF-8 byte limit not enforced: %#v, %v", got, err)
			}
		} else if err != nil || len(got.Entities) != 1 || got.Entities[0].Title != "界界界界" {
			t.Fatalf("inclusive UTF-8 boundary refused: %#v, %v", got, err)
		}
	}
}

func TestReadIndexAmbiguousShadowRowIDsRefuseSource(t *testing.T) {
	schema := strings.ReplaceAll(fixtureSchema, "rowid INTEGER PRIMARY KEY", "rowid INTEGER")
	for _, statement := range []string{
		"INSERT INTO Entities VALUES(20,2,'section-shadow','guid-shadow',NULL,NULL,NULL,NULL,NULL)",
		"INSERT INTO PageElements VALUES(81,'element-shadow','Shadow text',393230,20)",
	} {
		path := indexFixtureWithSchema(t, "shadow.db", schema, hierarchyFixture, statement)
		got, err := ReadIndex(context.Background(), path)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("ambiguous row-ID reference published: %#v, %v", got, err)
		}
	}
}

func TestReadIndexVirtualTableIsNotOrdinary(t *testing.T) {
	path := indexFixture(t, "virtual.db", hierarchyFixture, `
DROP TABLE Hashtags;
CREATE VIRTUAL TABLE Hashtags USING fts5(PageElementRowId);
INSERT INTO Hashtags(rowid,PageElementRowId) VALUES(1,81);`)
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("virtual-table marker published as ordinary source: %#v, %v", got, err)
	}
}
