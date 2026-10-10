package officeregistry

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const fixtureSchema = `
CREATE TABLE HKEY_CURRENT_USER(node_id INTEGER PRIMARY KEY,parent_id,name TEXT,write_time);
CREATE TABLE HKEY_CURRENT_USER_values(node_id INTEGER,name TEXT,type INTEGER,value);
`

const nativeFixture = `
INSERT INTO HKEY_CURRENT_USER VALUES
(1,-1,'HKEY_CURRENT_USER',NULL),(2,1,'Software',NULL),(3,2,'Microsoft',NULL),
(4,3,'Office',NULL),(5,4,'15.0',NULL),(6,5,'Common',NULL),(7,6,'MruUserData',NULL),
(8,7,'UnsignedUser',NULL),(9,8,'Word',NULL),(10,9,'Local',NULL),
(11,10,'Documents',NULL),(12,11,'native-document-key',NULL);
INSERT INTO HKEY_CURRENT_USER_values VALUES
(12,'FileName',1,'Fictional title'),(12,'DocumentUrl',1,'file:///fictional.docx'),
(12,'Path',1,'Friendly path'),(12,'Timestamp',1,'2026-10-09T12:34:56Z'),
(12,'Application',1,'independent application value'),(12,'FileSizeInBytes',11,0),
(12,'IsPinned',4,0);
`

func registryFixture(t *testing.T, name, schema string, statements ...string) string {
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

func TestReadIndexNativeDocuments(t *testing.T) {
	got, err := ReadIndex(context.Background(), registryFixture(t, "registry.db", fixtureSchema, nativeFixture))
	zero, unpinned := int64(0), false
	want := []Observation{{
		NodeID: 12, ApplicationPath: "Word", ApplicationValue: "independent application value",
		Title: "Fictional title", URL: "file:///fictional.docx", FriendlyPath: "Friendly path",
		OpenedRaw: "2026-10-09T12:34:56Z", Size: &zero, Pinned: &unpinned,
	}}
	if err != nil || !reflect.DeepEqual(got.Documents, want) || len(got.Losses) != 0 {
		t.Fatalf("native observations = %#v, %v; want %#v", got, err, want)
	}
}

func TestReadIndexExactNamespaceOnly(t *testing.T) {
	for _, statement := range []string{
		"UPDATE HKEY_CURRENT_USER SET name='UnsignedUser-ADAL' WHERE node_id=8",
		"UPDATE HKEY_CURRENT_USER SET name='ADAL' WHERE node_id=8",
		"UPDATE HKEY_CURRENT_USER SET name='OtherWord' WHERE node_id=9",
		"UPDATE HKEY_CURRENT_USER SET name='prefixSoftware' WHERE node_id=2",
		"UPDATE HKEY_CURRENT_USER SET name='DocumentsExtra' WHERE node_id=11",
	} {
		got, err := ReadIndex(context.Background(), registryFixture(t, "namespace.db", fixtureSchema, nativeFixture, statement))
		if err != nil || len(got.Documents) != 0 {
			t.Fatalf("unadmitted namespace published: %#v, %v", got, err)
		}
	}
}

func TestReadIndexEmptyOptionalAndRepeatedURLs(t *testing.T) {
	path := registryFixture(t, "optional.db", fixtureSchema, nativeFixture, `
DELETE FROM HKEY_CURRENT_USER_values WHERE name NOT IN ('FileName','DocumentUrl');
INSERT INTO HKEY_CURRENT_USER VALUES(13,11,'second-native-key',NULL);
INSERT INTO HKEY_CURRENT_USER_values VALUES(13,'FileName',1,'Repeated'),(13,'DocumentUrl',1,'file:///fictional.docx');`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Documents) != 2 || got.Documents[0].NodeID != 12 || got.Documents[1].NodeID != 13 ||
		got.Documents[0].Size != nil || got.Documents[0].Pinned != nil || got.Documents[0].OpenedRaw != "" ||
		got.Documents[0].URL != got.Documents[1].URL {
		t.Fatalf("optional/repeated native identities = %#v, %v", got, err)
	}
	empty, err := ReadIndex(context.Background(), registryFixture(t, "empty.db", fixtureSchema))
	if err != nil || len(empty.Documents) != 0 || len(empty.Losses) != 0 {
		t.Fatalf("empty valid registry = %#v, %v", empty, err)
	}
}

func TestReadIndexDuplicateSelectedValueRefusesAll(t *testing.T) {
	path := registryFixture(t, "duplicate.db", fixtureSchema, nativeFixture,
		"INSERT INTO HKEY_CURRENT_USER_values VALUES(12,'FileName',1,'Conflicting title')")
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("ambiguous native value selected: %#v, %v", got, err)
	}
}

func TestReadIndexNativeValueTypes(t *testing.T) {
	for _, statement := range []string{
		"UPDATE HKEY_CURRENT_USER_values SET value=7 WHERE name='IsPinned'",
		"UPDATE HKEY_CURRENT_USER_values SET type=1 WHERE name='FileSizeInBytes'",
		"UPDATE HKEY_CURRENT_USER_values SET value=-1 WHERE name='FileSizeInBytes'",
		"UPDATE HKEY_CURRENT_USER_values SET value='7' WHERE name='FileSizeInBytes'",
		"UPDATE HKEY_CURRENT_USER_values SET value=x'ff' WHERE name='DocumentUrl'",
		"UPDATE HKEY_CURRENT_USER_values SET value=123 WHERE name='FileName'",
		"UPDATE HKEY_CURRENT_USER_values SET value='bad'||char(0) WHERE name='FileName'",
	} {
		got, err := ReadIndex(context.Background(), registryFixture(t, "types.db", fixtureSchema, nativeFixture, statement))
		if err != nil || len(got.Documents) != 0 ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "office_registry_document_unmapped", Count: 1}}) {
			t.Fatalf("malformed registry value = %#v, %v", got, err)
		}
	}
}

func TestReadIndexAncestryLosses(t *testing.T) {
	for _, statement := range []string{
		"UPDATE HKEY_CURRENT_USER SET parent_id=999 WHERE node_id=12",
		"UPDATE HKEY_CURRENT_USER SET parent_id=12 WHERE node_id=12",
		"UPDATE HKEY_CURRENT_USER SET name='OtherRoot' WHERE node_id=1",
		"UPDATE HKEY_CURRENT_USER SET parent_id='not-an-id' WHERE node_id=12",
	} {
		got, err := ReadIndex(context.Background(), registryFixture(t, "ancestry.db", fixtureSchema, nativeFixture, statement))
		if err != nil || len(got.Documents) != 0 || len(got.Losses) == 0 {
			t.Fatalf("unsupported ancestry = %#v, %v", got, err)
		}
	}
}

func TestReadIndexUnsupportedSchemaRefusesAll(t *testing.T) {
	for _, schema := range []string{
		strings.Replace(fixtureSchema, "node_id INTEGER PRIMARY KEY", "node_id INTEGER", 1),
		strings.Replace(fixtureSchema, "name TEXT,type INTEGER,value", "renamed TEXT,type INTEGER,value", 1),
		"PRAGMA encoding='UTF-16le';" + fixtureSchema,
		strings.Split(fixtureSchema, "CREATE TABLE HKEY_CURRENT_USER_values")[0] +
			"CREATE VIRTUAL TABLE HKEY_CURRENT_USER_values USING fts5(node_id,name,type,value);",
	} {
		got, err := ReadIndex(context.Background(), registryFixture(t, "layout.db", schema))
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported registry layout = %#v, %v", got, err)
		}
	}
}

func TestReadIndexRetentionBoundsRefuseAll(t *testing.T) {
	path := registryFixture(t, "limits.db", fixtureSchema, nativeFixture)
	base := readLimits{fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20, nodes: 100, values: 100, depth: 128}
	for _, bound := range []string{"file", "field", "strings", "nodes", "values"} {
		limits := base
		switch bound {
		case "file":
			limits.fileBytes = 1
		case "field":
			limits.fieldBytes = 1
		case "strings":
			limits.stringBytes = 1
		case "nodes":
			limits.nodes = 1
		case "values":
			limits.values = 1
		}
		got, err := readIndex(context.Background(), path, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("retention bound %s = %#v, %v", bound, got, err)
		}
	}
	limits := base
	limits.depth = 1
	got, err := readIndex(context.Background(), path, limits)
	if err != nil || len(got.Documents) != 0 || len(got.Losses) == 0 {
		t.Fatalf("deep ancestry appeared valid: %#v, %v", got, err)
	}
}

func TestReadIndexNullOptionalAndUnselectedValues(t *testing.T) {
	path := registryFixture(t, "nulls.db", fixtureSchema, nativeFixture, `
UPDATE HKEY_CURRENT_USER_values SET value=NULL WHERE name IN ('Application','Timestamp','Path','FileSizeInBytes','IsPinned');
INSERT INTO HKEY_CURRENT_USER_values VALUES(12,'UnknownValue',1,'must not be exported'),
(999,'FileName',1,'unlinked value'),(12,'UnknownValue',1,'duplicate irrelevant value');
`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Documents) != 1 || got.Documents[0].Pinned != nil || got.Documents[0].Size != nil ||
		got.Documents[0].OpenedRaw != "" || len(got.Losses) != 0 {
		t.Fatalf("optional/unselected values = %#v, %v", got, err)
	}
}

func TestReadIndexLateStringBoundDiscardsAll(t *testing.T) {
	path := registryFixture(t, "late-limit.db", fixtureSchema, nativeFixture)
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 110, nodes: 12, values: 7, depth: 12,
	})
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("late selected-string limit published rows: %#v, %v", got, err)
	}
}

func TestReadIndexUnreadableAndPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ReadIndex(ctx, "unused"); err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled = %#v, %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "private-source-name.db")
	if err := os.WriteFile(path, []byte("not a SQLite registry"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-missing", filepath.Dir(path)} {
		got, err := ReadIndex(context.Background(), name)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-name") {
			t.Fatalf("unreadable source leaked output/error: %#v, %v", got, err)
		}
	}

}

func TestReadIndexEscapedReadOnlyCopy(t *testing.T) {
	name := "registry # % space.reg"
	if runtime.GOOS != "windows" {
		name = "registry # % ? space.reg"
	}
	path := registryFixture(t, name, fixtureSchema, nativeFixture)
	before, err := os.ReadFile(path) // #nosec G304 -- synthetic source in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Documents) != 1 {
		t.Fatalf("escaped read-only copy = %#v, %v", got, err)
	}
	after, err := os.ReadFile(path) // #nosec G304 -- synthetic source in t.TempDir.
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed source bytes")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("reader created %s sidecar", suffix)
		}
	}
}

func TestReadIndexApplicationScopesAndInclusiveBounds(t *testing.T) {
	path := registryFixture(t, "apps.db", fixtureSchema, nativeFixture, `
	INSERT INTO HKEY_CURRENT_USER VALUES
	(19,8,'Excel',NULL),(20,19,'Local',NULL),(21,20,'Documents',NULL),(22,21,'excel-key',NULL),
	(29,8,'PowerPoint',NULL),(30,29,'Local',NULL),(31,30,'Documents',NULL),(32,31,'ppt-key',NULL);
	INSERT INTO HKEY_CURRENT_USER_values VALUES
	(22,'FileName',1,'Excel title'),(22,'DocumentUrl',1,'raw-url'),
	(32,'FileName',1,'PowerPoint title'),(32,'DocumentUrl',1,'raw-url');`)
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20, nodes: 20, values: 11, depth: 12,
	})
	if err != nil || len(got.Documents) != 3 || got.Documents[0].ApplicationPath != "Word" ||
		got.Documents[1].ApplicationPath != "Excel" || got.Documents[2].ApplicationPath != "PowerPoint" {
		t.Fatalf("native app scope or inclusive bounds = %#v, %v", got, err)
	}
}

func TestReadIndexUnselectedValuesDoNotEnterCapture(t *testing.T) {
	large := strings.Repeat("x", 200)
	path := registryFixture(t, "excluded-values.db", fixtureSchema, nativeFixture,
		"INSERT INTO HKEY_CURRENT_USER_values VALUES(1,'FileName',1,'"+large+"'),(12,'UnselectedValue',1,'"+large+"')")
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 100, stringBytes: 1000, nodes: 100, values: 100, depth: 128,
	})
	if err != nil || len(got.Documents) != 1 || len(got.Losses) != 0 {
		t.Fatalf("outside-namespace/unselected data affected document capture: %#v, %v", got, err)
	}
}

func TestReadIndexMalformedExistingNegativeParentCannotBecomeRoot(t *testing.T) {
	for _, statement := range []string{
		"UPDATE HKEY_CURRENT_USER SET node_id=-1,parent_id='malformed' WHERE node_id=1; UPDATE HKEY_CURRENT_USER SET parent_id=-1 WHERE node_id=2",
		"UPDATE HKEY_CURRENT_USER SET node_id=-1,name=x'ff' WHERE node_id=1; UPDATE HKEY_CURRENT_USER SET parent_id=-1 WHERE node_id=2",
	} {
		got, err := ReadIndex(context.Background(), registryFixture(t, "malformed-root.db", fixtureSchema, nativeFixture, statement))
		if err != nil || len(got.Documents) != 0 || len(got.Losses) == 0 {
			t.Fatalf("existing malformed parent selected as absent root: %#v, %v", got, err)
		}
	}
}

func TestReadIndexUnselectedCollationAliasCannotAffectCapture(t *testing.T) {
	schema := strings.Replace(fixtureSchema, "node_id INTEGER,name TEXT,type", "node_id INTEGER,name TEXT COLLATE NOCASE,type", 1)
	path := registryFixture(t, "collation.db", schema, nativeFixture,
		"INSERT INTO HKEY_CURRENT_USER_values VALUES(12,'filename',1,'"+strings.Repeat("x", 200)+"')")
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 100, stringBytes: 1000, nodes: 100, values: 100, depth: 128,
	})
	if err != nil || len(got.Documents) != 1 || got.Documents[0].Title != "Fictional title" || len(got.Losses) != 0 {
		t.Fatalf("unselected collation alias affected capture: %#v, %v", got, err)
	}
}
