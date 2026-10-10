package onedrivelists

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const siteA = "11111111-1111-1111-1111-111111111111"
const siteB = "22222222-2222-2222-2222-222222222222"
const listA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

const scopeSchema = `CREATE TABLE lists(listID TEXT,webID TEXT,siteID TEXT,driveID TEXT,title TEXT,siteUrl TEXT,listUrl TEXT,lastSyncTime INTEGER,PRIMARY KEY(listID,siteID));`
const itemSchema = `(ID INTEGER,UniqueId TEXT,FileRef TEXT,FileLeafRef TEXT,FSObjType TEXT,Author INTEGER,Editor INTEGER,Created TEXT,Modified TEXT,Last_x0020_Modified TEXT,ServerUrl TEXT,File_x0020_Type TEXT)`
const userSchema = `(id INTEGER,key TEXT,value TEXT,PRIMARY KEY(id,key))`

func fixture(t *testing.T, statements ...string) string {
	t.Helper()
	return fixtureWithSchema(t, scopeSchema, statements...)
}

func fixtureWithSchema(t *testing.T, schema string, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.db")
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, sql := range statements {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func inventoryTable(site string) string { return "list_" + listA + "_" + site + "_rows" }
func usersTable(site string) string     { return "site_" + site + "_users" }

func nativeScope(site string) string {
	return "INSERT INTO lists VALUES('" + listA + "','raw-web','" + site + "','drive','Title','site-url','list-url',123);"
}

func TestReadIndexScopedUserEvidence(t *testing.T) {
	sql := ""
	for _, site := range []string{siteB, siteA} {
		sql += nativeScope(site) +
			`CREATE TABLE "` + inventoryTable(site) + `"` + itemSchema + `;` +
			`CREATE TABLE "` + usersTable(site) + `"` + userSchema + `;` +
			`INSERT INTO "` + inventoryTable(site) + `" VALUES(7,'native-unique','/fictional/path','Name','0',1,1,'created','modified','last-modified','server-url','docx');` +
			`INSERT INTO "` + usersTable(site) + `" VALUES(1,'Title','Person'),(1,'EMail','` + site + `@example.invalid'),(1,'Name','native-name'),(1,'SipAddress','sip');`
	}
	got, err := ReadIndex(context.Background(), fixture(t, sql))
	if err != nil || len(got.Scopes) != 2 || len(got.Items) != 2 || len(got.Users) != 2 || len(got.Losses) != 0 {
		t.Fatalf("scoped capture = %#v,%v", got, err)
	}
	for i, site := range []string{siteA, siteB} {
		if got.Scopes[i].SiteID != site || !got.Scopes[i].InventoryPresent || got.Items[i].SiteID != site ||
			got.Items[i].UniqueID != "native-unique" || got.Items[i].Kind != "file" || got.Items[i].CreatedRaw != "created" ||
			got.Items[i].AuthorID == nil || *got.Items[i].AuthorID != 1 || got.Users[i].SiteID != site ||
			got.Users[i].Email != site+"@example.invalid" {
			t.Fatalf("site-qualified identity/user join lost: %#v", got)
		}
	}
}

func TestReadIndexUnavailableScopeIsolation(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+nativeScope(siteB)+
		`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;`+
		`CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Scopes) != 2 || !got.Scopes[0].InventoryPresent || got.Scopes[1].InventoryPresent ||
		len(got.Items) != 0 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_lists_inventory_unavailable", Count: 1}, {Code: "onedrive_lists_users_unavailable", Count: 1}}) {
		t.Fatalf("unavailable/empty peer distinction lost: %#v,%v", got, err)
	}
}

func TestReadIndexMetadataNamesAreFinite(t *testing.T) {
	got, err := ReadIndex(context.Background(), fixture(t,
		`INSERT INTO lists VALUES('bad"; DROP TABLE lists;--','web','`+siteA+`',NULL,NULL,NULL,NULL,NULL);`))
	if err != nil || len(got.Scopes) != 0 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_lists_scope_unmapped", Count: 1}}) {
		t.Fatalf("unvalidated metadata name selected: %#v,%v", got, err)
	}
}

func TestReadIndexAmbiguousInventoryRefusesAll(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+
		`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;`+
		`CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`+
		`INSERT INTO "`+inventoryTable(siteA)+`" VALUES
(7,'first','p','A','0',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
(7,'second','p','B','0',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`)
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("ambiguous native inventory ID selected by order: %#v,%v", got, err)
	}
}

func TestReadIndexUnrecognizedMetadataKeysRefuseAll(t *testing.T) {
	for _, schema := range []string{
		strings.Replace(scopeSchema, "PRIMARY KEY(listID,siteID)", "PRIMARY KEY(siteID,listID)", 1),
		strings.Replace(scopeSchema, ",PRIMARY KEY(listID,siteID)", "", 1),
		"PRAGMA encoding='UTF-16le';" + scopeSchema,
	} {
		got, err := ReadIndex(context.Background(), fixtureWithSchema(t, schema))
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported metadata identity schema accepted: %#v,%v", got, err)
		}
	}

}

func TestReadIndexMalformedRowsAndUnknownReferences(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+
		`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;`+
		`CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`+
		`INSERT INTO "`+usersTable(siteA)+`" VALUES(1,'Title',x'ff');`+
		`INSERT INTO "`+inventoryTable(siteA)+`" VALUES
	(1,'folder','p','Folder','1',88,88,NULL,NULL,NULL,NULL,NULL),
	(2,'unmapped','p','Bad','other',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{{Code: "onedrive_lists_item_unmapped", Count: 1}, {Code: "onedrive_lists_user_reference_unknown", Count: 2}, {Code: "onedrive_lists_user_unmapped", Count: 1}}
	if err != nil || len(got.Items) != 1 || got.Items[0].Kind != "folder" || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("malformed/reference observations = %#v,%v", got, err)
	}
}

func TestReadIndexDuplicateUniqueIdentityRefusesAll(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`+
		`INSERT INTO "`+inventoryTable(siteA)+`" VALUES
	(1,'same','p','A','0',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
	(2,'same','p','B','other',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`)
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("ambiguous scoped UniqueID selected: %#v,%v", got, err)
	}
}

func TestReadIndexOptionalSchemaAbsenceIsCounted(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"(ID INTEGER);CREATE VIRTUAL TABLE "`+usersTable(siteA)+`" USING fts5(id,key,value);`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Scopes) != 1 || got.Scopes[0].InventoryPresent ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_lists_inventory_unavailable", Count: 1}, {Code: "onedrive_lists_users_unavailable", Count: 1}}) {
		t.Fatalf("optional unsupported layout concealed: %#v,%v", got, err)
	}
}

func TestReadIndexRetentionBoundsRefuseAll(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`+
		`INSERT INTO "`+usersTable(siteA)+`" VALUES(1,'Title','Person');INSERT INTO "`+inventoryTable(siteA)+`" VALUES(1,'u','p','Name','0',1,1,NULL,NULL,NULL,NULL,NULL);`)
	base := readLimits{fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20, scopes: 1, items: 1, users: 1}
	got, err := readIndex(context.Background(), path, base)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("inclusive row bounds refused: %#v,%v", got, err)
	}
	for _, bound := range []string{"file", "field", "strings", "scopes", "items", "users"} {
		limits := base
		switch bound {
		case "file":
			limits.fileBytes = 1
		case "field":
			limits.fieldBytes = 1
		case "strings":
			limits.stringBytes = 1
		case "scopes":
			limits.scopes = 0
		case "items":
			limits.items = 0
		case "users":
			limits.users = 0
		}
		got, err := readIndex(context.Background(), path, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s bound published partial source: %#v,%v", bound, got, err)
		}
	}
}

func TestReadIndexUnreadableSourcesAreSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-source-value.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{path, path + "-missing", filepath.Dir(path)} {
		got, err := ReadIndex(context.Background(), source)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-value") {
			t.Fatalf("unsafe source: %#v,%v", got, err)
		}
	}

}

func TestReadIndexGeneratedSelectedMetadataRefusesAll(t *testing.T) {
	schema := strings.Replace(scopeSchema, "title TEXT", "securityToken TEXT,title TEXT GENERATED ALWAYS AS (securityToken) VIRTUAL", 1)
	path := fixtureWithSchema(t, schema,
		`INSERT INTO lists(listID,webID,siteID,securityToken) VALUES('`+listA+`','web','`+siteA+`','SYNTHETIC-EXCLUDED');`)
	got, err := ReadIndex(context.Background(), path)
	if err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("generated metadata exported excluded field: %#v,%v", got, err)
	}
}

func TestReadIndexGeneratedSelectedOptionalColumnsStayUnavailable(t *testing.T) {
	for _, kind := range []string{"users", "inventory"} {
		t.Run(kind, func(t *testing.T) {
			schema := nativeScope(siteA) + nativeScope(siteB) +
				`CREATE TABLE "` + inventoryTable(siteB) + `"` + itemSchema + `;` +
				`CREATE TABLE "` + usersTable(siteB) + `"` + userSchema + `;` +
				`INSERT INTO "` + inventoryTable(siteB) + `" VALUES(7,'healthy','p','Healthy','0',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`
			var want []Loss
			if kind == "users" {
				schema += `CREATE TABLE "` + inventoryTable(siteA) + `"` + itemSchema + `;` +
					`CREATE TABLE "` + usersTable(siteA) + `"(id INTEGER,key TEXT,securityToken TEXT,value TEXT GENERATED ALWAYS AS (securityToken) VIRTUAL,PRIMARY KEY(id,key));` +
					`INSERT INTO "` + usersTable(siteA) + `"(id,key,securityToken) VALUES(1,'Title','SYNTHETIC-EXCLUDED');`
				want = []Loss{{Code: "onedrive_lists_users_unavailable", Count: 1}}
			} else {
				generated := strings.Replace(itemSchema, "FileLeafRef TEXT", "securityToken TEXT,FileLeafRef TEXT GENERATED ALWAYS AS (securityToken) STORED", 1)
				schema += `CREATE TABLE "` + usersTable(siteA) + `"` + userSchema + `;` +
					`CREATE TABLE "` + inventoryTable(siteA) + `"` + generated + `;` +
					`INSERT INTO "` + inventoryTable(siteA) + `"(ID,UniqueId,securityToken,FSObjType) VALUES(1,'u','SYNTHETIC-EXCLUDED','0');`
				want = []Loss{{Code: "onedrive_lists_inventory_unavailable", Count: 1}}
			}
			got, err := ReadIndex(context.Background(), fixture(t, schema))
			if err != nil || len(got.Items) != 1 || got.Items[0].SiteID != siteB || got.Items[0].Name != "Healthy" ||
				len(got.Users) != 0 || !reflect.DeepEqual(got.Losses, want) {
				t.Fatalf("generated %s escaped availability boundary: %#v,%v", kind, got, err)
			}
		})
	}
}

func TestReadIndexGeneratedUnselectedColumnsRemainUnconsumed(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+
		`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;`+
		`CREATE TABLE "`+usersTable(siteA)+`"(id INTEGER,key TEXT,value TEXT,unused TEXT GENERATED ALWAYS AS (zeroblob(1048577)) VIRTUAL,PRIMARY KEY(id,key));`+
		`INSERT INTO "`+usersTable(siteA)+`"(id,key,value) VALUES(1,'Title','Selected');`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Users) != 1 || got.Users[0].Title != "Selected" || len(got.Losses) != 0 {
		t.Fatalf("unselected generated expression consumed: %#v,%v", got, err)
	}
}
