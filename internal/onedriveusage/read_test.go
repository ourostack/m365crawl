package onedriveusage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const fixtureSchema = `
CREATE TABLE recent_files_spo(ID TEXT PRIMARY KEY,ItemId TEXT,ListId TEXT,WebId TEXT,SiteId TEXT,
LastModifiedDateTime,LastAccessedDateTime,FileModifiedDateTime,LastSharedDateTime,SavedDateTime,
Type TEXT,Source TEXT,Extension TEXT,WorkingSetId TEXT,HiddenFromShared INTEGER,IsLocalPlaceHolder INTEGER);
CREATE TABLE recent_files_formatted_spo(ID TEXT,Format TEXT NOT NULL,FormattedValue TEXT NOT NULL,PRIMARY KEY(ID,Format));
CREATE TABLE top_collaborators(ID TEXT PRIMARY KEY,FormattedValue TEXT);
`

func usageFixture(t *testing.T, schema string, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
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
	document := escapedBody(t, `{"file":{"FileName":"A","SharePointItem":{"SiteId":"s","WebId":"w","ListId":"l","UniqueId":"u"},
 "AllExtensions":{"SharingHistory":{"Instances":[{"SharedByTime":"raw","Participants":[{"DisplayName":"Person","Smtp":"p@example.invalid"}]}]}}}}`)
	if _, err := db.Exec(`INSERT INTO recent_files_formatted_spo VALUES('id','variant-b',?),('id','variant-a',?)`, document, document); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO top_collaborators VALUES('native-person','{"Id":"stated-person","DisplayName":"Person","EmailAddresses":["p@example.invalid"]}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const recentFixture = `INSERT INTO recent_files_spo VALUES
('id','native-item','native-list','native-web','native-site','modified','accessed',NULL,'shared',NULL,
'Word','native-source','docx','working-set',0,1);`

func TestReadIndexRetainsCompositeVariants(t *testing.T) {
	got, err := ReadIndex(context.Background(), usageFixture(t, fixtureSchema, recentFixture))
	hidden, placeholder := int64(0), int64(1)
	want := []RecentObservation{{
		ID: "id", ItemID: "native-item", ListID: "native-list", WebID: "native-web", SiteID: "native-site",
		LastModifiedRaw: "modified", LastAccessedRaw: "accessed", LastSharedRaw: "shared",
		Type: "Word", Source: "native-source", Extension: "docx", WorkingSetID: "working-set",
		HiddenFromShared: &hidden, LocalPlaceholder: &placeholder,
	}}
	if err != nil || !reflect.DeepEqual(got.Recent, want) || len(got.Documents) != 2 ||
		got.Documents[0].Format != "variant-a" || got.Documents[1].Format != "variant-b" ||
		got.Documents[0].ID != "id" || got.Documents[0].SiteID != "s" ||
		got.Documents[0].UniqueID != "u" || len(got.Documents[0].History) != 1 ||
		got.Documents[0].History[0].AtRaw != "raw" || len(got.Documents[0].History[0].Participants) != 1 ||
		len(got.Collaborators) != 1 || got.Collaborators[0].ID != "native-person" ||
		got.Collaborators[0].UserID != "stated-person" || len(got.Losses) != 0 {
		t.Fatalf("native usage capture = %#v,%v", got, err)
	}
}

func TestReadIndexNativeStorageAndOrphanLosses(t *testing.T) {
	got, err := ReadIndex(context.Background(), usageFixture(t, fixtureSchema,
		`INSERT INTO recent_files_spo(ID,ItemId) VALUES('id',x'ff');`))
	want := []Loss{{Code: "onedrive_usage_document_orphan", Count: 2}, {Code: "onedrive_usage_recent_unmapped", Count: 1}}
	if err != nil || len(got.Recent) != 0 || len(got.Documents) != 0 || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("malformed recent/join = %#v,%v", got, err)
	}
}

func TestReadIndexLimitsRefuseAll(t *testing.T) {
	path := usageFixture(t, fixtureSchema, recentFixture)
	base := readLimits{fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20,
		recent: 1, documents: 2, collaborators: 1, history: 2, participants: 2}
	got, err := readIndex(context.Background(), path, base)
	if err != nil || len(got.Documents) != 2 {
		t.Fatalf("inclusive limits = %#v,%v", got, err)
	}
	for _, bound := range []string{"file", "field", "strings", "recent", "documents", "collaborators", "history", "participants"} {
		limits := base
		switch bound {
		case "file":
			limits.fileBytes = 1
		case "field":
			limits.fieldBytes = 1
		case "strings":
			limits.stringBytes = 1
		case "recent":
			limits.recent = 0
		case "documents":
			limits.documents = 1
		case "collaborators":
			limits.collaborators = 0
		case "history":
			limits.history = 1
		case "participants":
			limits.participants = 1
		}
		got, err := readIndex(context.Background(), path, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s limit retained partial source: %#v,%v", bound, got, err)
		}
	}

}

func TestReadIndexOrphanRowsRetainOnlyCountedLosses(t *testing.T) {
	path := usageFixture(t, fixtureSchema, "CREATE TRIGGER remove_collab AFTER INSERT ON top_collaborators BEGIN DELETE FROM top_collaborators; END;")
	got, err := readIndex(context.Background(), path, readLimits{
		fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 0,
		recent: 100, documents: 100, collaborators: 100, history: 100, participants: 100,
	})
	if err != nil || len(got.Documents) != 0 || len(got.Recent) != 0 || len(got.Collaborators) != 0 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_usage_document_orphan", Count: 2}}) {
		t.Fatalf("orphan IDs affected retained payload budget: %#v,%v", got, err)
	}
}

func changeFixture(t *testing.T, path string, statement string) {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadIndexMalformedPayloadsHaveExplicitLosses(t *testing.T) {
	path := usageFixture(t, fixtureSchema, recentFixture)
	changeFixture(t, path, `
UPDATE recent_files_formatted_spo SET FormattedValue='bad-json' WHERE Format='variant-a';
UPDATE recent_files_formatted_spo SET Format='' WHERE Format='variant-b';
UPDATE top_collaborators SET FormattedValue='[]';
INSERT INTO top_collaborators VALUES('empty',NULL);`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{{Code: "onedrive_usage_collaborator_unmapped", Count: 2}, {Code: "onedrive_usage_document_unmapped", Count: 2}}
	if err != nil || len(got.Recent) != 1 || len(got.Documents) != 0 || len(got.Collaborators) != 0 ||
		!reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("payload losses = %#v,%v", got, err)
	}
}

func TestReadIndexEmptyAndUnsupportedLayouts(t *testing.T) {
	path := usageFixture(t, fixtureSchema)
	changeFixture(t, path, `DELETE FROM recent_files_formatted_spo; DELETE FROM top_collaborators;`)
	got, err := ReadIndex(context.Background(), path)
	if err != nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("valid empty index = %#v,%v", got, err)
	}
	for _, schema := range []string{
		"PRAGMA encoding='UTF-16le';" + fixtureSchema,
		strings.Replace(fixtureSchema, "ID TEXT PRIMARY KEY", "ID TEXT", 1),
		strings.Replace(fixtureSchema, "PRIMARY KEY(ID,Format)", "PRIMARY KEY(Format,ID)", 1),
	} {
		path := usageFixture(t, schema, recentFixture)
		got, err := ReadIndex(context.Background(), path)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported native schema admitted: %#v,%v", got, err)
		}
	}
}

func TestReadIndexLateStringRetentionAndNullableState(t *testing.T) {
	path := usageFixture(t, fixtureSchema, recentFixture)
	changeFixture(t, path, `UPDATE recent_files_spo SET HiddenFromShared=NULL,IsLocalPlaceHolder=NULL;`)
	base := readLimits{fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20,
		recent: 100, documents: 100, collaborators: 100, history: 100, participants: 100}
	got, err := readIndex(context.Background(), path, base)
	if err != nil || len(got.Recent) != 1 || got.Recent[0].HiddenFromShared != nil || got.Recent[0].LocalPlaceholder != nil {
		t.Fatalf("unknown states substituted: %#v,%v", got, err)
	}
	for _, bytes := range []int64{100, 180, 230} {
		limits := base
		limits.stringBytes = bytes
		got, err := readIndex(context.Background(), path, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("late string limit %d retained partial capture: %#v,%v", bytes, got, err)
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
			t.Fatalf("unsafe source error: %#v,%v", got, err)
		}
	}

}

func TestReadIndexDecodedLatestAndFieldRetention(t *testing.T) {
	path := usageFixture(t, fixtureSchema, recentFixture)
	raw := escapedBody(t, `{"file":{"ItemProperties":{"Shared":{"SubjectProperty":"latest subject"}}}}`)
	changeFixture(t, path, "UPDATE recent_files_formatted_spo SET FormattedValue='"+strings.ReplaceAll(raw, "'", "''")+"'")
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Documents) != 2 || got.Documents[0].Latest == nil || got.Documents[0].Latest.Subject != "latest subject" {
		t.Fatalf("latest source evidence lost: %#v,%v", got, err)
	}
}

func TestReadIndexNativeDateDeclarationsPreserveRawStorage(t *testing.T) {
	schema := strings.Replace(fixtureSchema, "LastModifiedDateTime,", "LastModifiedDateTime DATETIME,", 1)
	schema = strings.Replace(schema, "LastAccessedDateTime,", "LastAccessedDateTime DATETIME,", 1)
	path := usageFixture(t, schema, strings.ReplaceAll(recentFixture, "'modified','accessed'",
		"'2026-10-09T12:34:56Z','2026-10-08T01:02:03Z'"))
	got, err := ReadIndex(context.Background(), path)
	if err != nil || len(got.Recent) != 1 || got.Recent[0].LastModifiedRaw != "2026-10-09T12:34:56Z" ||
		got.Recent[0].LastAccessedRaw != "2026-10-08T01:02:03Z" || len(got.Documents) != 2 || len(got.Losses) != 0 {
		t.Fatalf("declared date coerced native storage or lost joins: %#v,%v", got, err)
	}
}

func TestReadIndexJSONStructureLimitsDiscardEarlierRows(t *testing.T) {
	for _, kind := range []string{"documents-depth", "documents-members", "collaborators-depth", "collaborators-members"} {
		path := usageFixture(t, fixtureSchema, recentFixture)
		var raw string
		if strings.HasSuffix(kind, "depth") {
			raw = `{"file":{},"extra":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + `}`
		} else {
			raw = `{"file":{},"extra":[` + strings.Repeat("0,", 131071) + `0]}`
		}
		if strings.HasPrefix(kind, "documents") {
			body := escapedBody(t, raw)
			changeFixture(t, path, "UPDATE recent_files_formatted_spo SET FormattedValue='"+strings.ReplaceAll(body, "'", "''")+"' WHERE Format='variant-b'")
		} else {
			changeFixture(t, path, "UPDATE top_collaborators SET FormattedValue='"+strings.ReplaceAll(raw, "'", "''")+"'")
		}
		got, err := ReadIndex(context.Background(), path)
		var fixed *ReadError
		if !errors.As(err, &fixed) || fixed.Code != "onedrive_usage_index_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s structure bound retained partial source: %#v,%v", kind, got, err)
		}
	}
}

func TestReadIndexJSONStructureLimitsInclusive(t *testing.T) {
	for _, raw := range []string{
		`{"file":{},"extra":` + strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63) + `}`,
		`{"file":{},"extra":[` + strings.Repeat("0,", 131069) + `0]}`,
	} {
		path := usageFixture(t, fixtureSchema, recentFixture)
		body := escapedBody(t, raw)
		changeFixture(t, path, "UPDATE recent_files_formatted_spo SET FormattedValue='"+strings.ReplaceAll(body, "'", "''")+"' WHERE Format='variant-b'")
		changeFixture(t, path, "UPDATE top_collaborators SET FormattedValue='"+strings.ReplaceAll(raw, "'", "''")+"'")
		got, err := ReadIndex(context.Background(), path)
		if err != nil || len(got.Documents) != 2 || len(got.Collaborators) != 1 || len(got.Losses) != 0 {
			t.Fatalf("inclusive JSON structure limit refused valid source: %#v,%v", got, err)
		}
	}
}
