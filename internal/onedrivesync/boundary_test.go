package onedrivesync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReadIndexUnsupportedSchemaRefusesAll(t *testing.T) {
	for _, schema := range []string{
		strings.Replace(nativeSchema, "scopeID TEXT PRIMARY KEY", "scopeID TEXT", 1),
		strings.Replace(nativeSchema, "resourceID TEXT PRIMARY KEY", "resourceID INTEGER PRIMARY KEY", 1),
		strings.Replace(nativeSchema, "PRIMARY KEY(siteID,webID,listID)", "PRIMARY KEY(webID,siteID,listID)", 1),
		"PRAGMA encoding='UTF-16le';" + nativeSchema,
		strings.Replace(nativeSchema, "webURL TEXT", "excluded TEXT,webURL TEXT GENERATED ALWAYS AS (excluded) VIRTUAL", 1),
		strings.Replace(nativeSchema, "modifiedBy TEXT", "excluded TEXT,modifiedBy TEXT GENERATED ALWAYS AS (excluded) STORED", 1),
		strings.Replace(nativeSchema, "CREATE TABLE od_HydrationData(resourceID TEXT PRIMARY KEY,firstHydrationTime INTEGER,lastHydrationTime INTEGER,hydrationCount INTEGER,lastHydrationType TEXT);",
			"CREATE VIRTUAL TABLE od_HydrationData USING fts5(resourceID,firstHydrationTime,lastHydrationTime,hydrationCount,lastHydrationType);", 1),
	} {
		got, err := ReadIndex(context.Background(), fixture(t, schema, ""))
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_unsupported" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported native schema admitted: %#v,%v", got, err)
		}
	}
}

func TestReadIndexMalformedObservationIsCounted(t *testing.T) {
	for _, value := range []string{"x'ff'", "CAST(x'ff' AS TEXT)", "CAST('42' AS BLOB)"} {
		rows := strings.Replace(nativeRows, "'Creator'", value, 1)
		got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, rows))
		if err != nil || len(got.Files) != 1 || len(got.Graph) != 0 ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_sync_graph_unmapped", Count: 1}}) {
			t.Fatalf("malformed graph observation concealed/fatal: %#v,%v", got, err)
		}
	}
}

func TestReadIndexBoundsRefuseAll(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	base := readLimits{info.Size(), 1 << 20, 1 << 20, [6]int{1, 1, 1, 1, 1, 1}, 1}
	got, err := readIndex(context.Background(), path, base)
	if err != nil || len(got.Files) != 1 {
		t.Fatalf("inclusive input bounds refused: %v", err)
	}
	for bound := 0; bound < 10; bound++ {
		limits := base
		switch bound {
		case 0:
			limits.fileBytes--
		case 1:
			limits.fieldBytes = 1
		case 2:
			limits.stringBytes = 1
		case 3:
			limits.ancestry = 0
		default:
			limits.rows[bound-4] = 0
		}
		got, err := readIndex(context.Background(), path, limits)
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("bound %d published partial success: %v", bound, err)
		}
	}
}

func TestReadIndexLogicalStringCharging(t *testing.T) {
	for _, test := range []struct {
		rows  string
		bytes int64
	}{
		{`INSERT INTO od_GraphMetadata_Records VALUES('id','x','x','x');`, 5},
		{`INSERT INTO od_GraphMetadata_Records VALUES('id',x'ff',NULL,NULL);`, 2},
		{`INSERT INTO od_ClientPolicy_Records VALUES('ab','c','d',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`, 4},
	} {
		path := fixture(t, nativeSchema, test.rows)
		limits := readLimits{1 << 20, 1 << 20, test.bytes, [6]int{1, 1, 1, 1, 1, 1}, 256}
		if _, err := readIndex(context.Background(), path, limits); err != nil {
			t.Fatalf("inclusive logical-string budget refused: %v", err)
		}
		limits.stringBytes--
		got, err := readIndex(context.Background(), path, limits)
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("logical strings ignored rejected key or repeated nonidentity occurrence: %v", err)
		}
	}
}

func TestReadIndexRejectedRowsStillConsumeBounds(t *testing.T) {
	path := fixture(t, nativeSchema, `INSERT INTO od_ScopeInfo_Records VALUES(NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL),(NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);`)
	limits := readLimits{1 << 20, 1 << 20, 1 << 20, [6]int{1, 1, 1, 1, 1, 1}, 256}
	if got, err := readIndex(context.Background(), path, limits); err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("rejected rows bypassed native source row cap: %v", err)
	}
	path = fixture(t, nativeSchema, `INSERT INTO od_GraphMetadata_Records VALUES('id',zeroblob(8),NULL,NULL);`)
	limits.fieldBytes = 8
	if _, err := readIndex(context.Background(), path, limits); err != nil {
		t.Fatalf("inclusive malformed field cap refused: %v", err)
	}
	limits.fieldBytes = 7
	if got, err := readIndex(context.Background(), path, limits); err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("rejected row bypassed predecode field cap: %v", err)
	}
}

func TestReadIndexMalformedNativeTypes(t *testing.T) {
	for _, test := range []struct {
		statement, kind string
	}{
		{`UPDATE od_ScopeInfo_Records SET sourceResourceID=x'ff';`, "scope"},
		{`UPDATE od_ClientFile_Records SET size='opaque';`, "file"},
		{`UPDATE od_HydrationData SET lastHydrationType=x'ff';`, "hydration"},
		{`UPDATE od_HydrationData SET hydrationCount='opaque';`, "hydration"},
		{`INSERT INTO od_GraphMetadata_Records VALUES(x'ff',NULL,NULL,NULL);`, "graph"},
	} {
		got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, nativeRows+test.statement))
		found := false
		for _, loss := range got.Losses {
			if loss.Code == "onedrive_sync_"+test.kind+"_unmapped" && loss.Count == 1 {
				found = true
			}
		}
		if err != nil || !found {
			t.Fatalf("native malformed %s became unknown/zero without loss: %v,%v", test.kind, got.Losses, err)
		}
	}
}

func TestReadIndexEveryAdmittedStringConsumesBudget(t *testing.T) {
	for _, test := range []struct {
		rows  string
		bytes int64
	}{
		{`INSERT INTO od_ScopeInfo_Records VALUES('i','s',NULL,NULL,NULL,NULL,NULL,NULL);`, 1},
		{`INSERT INTO od_ClientFile_Records VALUES('i','p','n',NULL,NULL,NULL,NULL,NULL);`, 1},
		{`INSERT INTO od_ClientFolder_Records VALUES('i','p',NULL,'n');`, 1},
		{`INSERT INTO od_GraphMetadata_Records VALUES('i','c',NULL,NULL);`, 1},
		{`INSERT INTO od_ClientPolicy_Records VALUES('a','b','c',NULL,NULL,'l',NULL,NULL,NULL,NULL);`, 3},
		{`INSERT INTO od_HydrationData VALUES('i',NULL,NULL,NULL,'t');`, 1},
	} {
		path := fixture(t, nativeSchema, test.rows)
		limits := readLimits{1 << 20, 1 << 20, test.bytes, [6]int{1, 1, 1, 1, 1, 1}, 256}
		got, err := readIndex(context.Background(), path, limits)
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("admitted string occurrence escaped budget: %v", err)
		}
	}
}

func TestReadIndexMissingRequiredTableRefusesAll(t *testing.T) {
	got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, `DROP TABLE od_HydrationData;`))
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_unsupported" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("missing required table published empty success: %v", err)
	}
}

type checkedContext struct {
	context.Context
	calls, cancelAt int
}

func (c *checkedContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestNativeRelationshipWalkCancellation(t *testing.T) {
	result := Result{Scopes: []Scope{{ID: "scope"}}, Folders: []Folder{{ID: "folder", ParentID: "scope"}}, Files: []File{{ID: "file", ParentID: "folder"}}}
	for _, at := range []int{1, 3} {
		ctx := &checkedContext{Context: context.Background(), cancelAt: at}
		err := relationships(ctx, &result, map[string]int{}, 256)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("native ancestry cancellation discarded: %v", err)
		}
	}
}

func TestReadIndexAncestryDepthBoundary(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		for _, count := range []int{256, 257} {
			var rows strings.Builder
			rows.WriteString(nativeRows)
			for i := 0; i < count; i++ {
				parent := fmt.Sprintf("f%03d", i+1)
				if i == count-1 {
					parent = "scope"
					if cycle {
						parent = "f000"
					}
				}
				fmt.Fprintf(&rows, "INSERT INTO od_ClientFolder_Records VALUES('f%03d','%s',NULL,NULL);", i, parent)
			}
			rows.WriteString(`INSERT INTO od_ClientFile_Records VALUES('deep','f000',NULL,NULL,NULL,NULL,NULL,NULL);`)
			got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, rows.String()))
			if count == 257 {
				var typed *ReadError
				if !errors.As(err, &typed) || typed.Code != "onedrive_sync_index_too_large" || !reflect.DeepEqual(got, Result{}) {
					t.Fatalf("257-folder ancestry cycle=%v accepted: %v", cycle, err)
				}
			} else {
				var want []Loss
				if cycle {
					want = []Loss{{Code: "onedrive_sync_parent_cycle", Count: 256}, {Code: "onedrive_sync_parent_unresolved", Count: 1}}
				}
				if err != nil || len(got.Folders) != 257 || len(got.Files) != 2 || !reflect.DeepEqual(got.Losses, want) {
					t.Fatalf("inclusive 256-folder ancestry cycle=%v lost: %v,%v", cycle, got.Losses, err)
				}
			}
		}
	}
}

func TestReadIndexSelfCycleAndWrongKindReferences(t *testing.T) {
	path := fixture(t, nativeSchema, nativeRows+`
INSERT INTO od_ClientFolder_Records VALUES('self','self','scope','Self');
INSERT INTO od_ClientFile_Records VALUES('tail','self',NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO od_GraphMetadata_Records VALUES('scope',NULL,NULL,NULL);
INSERT INTO od_HydrationData VALUES('scope',NULL,NULL,NULL,NULL);
`)
	got, err := ReadIndex(context.Background(), path)
	want := []Loss{
		{Code: "onedrive_sync_graph_orphan", Count: 1},
		{Code: "onedrive_sync_hydration_orphan", Count: 1},
		{Code: "onedrive_sync_parent_cycle", Count: 1},
		{Code: "onedrive_sync_parent_unresolved", Count: 1},
	}
	if err != nil || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("self-cycle/scope-only references selected authority: %v,%v", got.Losses, err)
	}
}

func TestReadIndexNativeTupleIdentityAndOrder(t *testing.T) {
	first := `INSERT INTO od_ClientPolicy_Records VALUES('ab','c','d',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`
	second := `INSERT INTO od_ClientPolicy_Records VALUES('a','bc','d',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`
	a, err := ReadIndex(context.Background(), fixture(t, nativeSchema, first+second))
	if err != nil || len(a.Policies) != 2 || a.Policies[0].SiteID != "a" || len(a.Losses) != 0 {
		t.Fatalf("tuple collision or native ordering lost: %#v,%v", a.Policies, err)
	}
	b, err := ReadIndex(context.Background(), fixture(t, nativeSchema, second+first))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("tuple order depends on insertion: %#v,%v", b.Policies, err)
	}
	got, err := ReadIndex(context.Background(), fixture(t, nativeSchema, nativeRows+`UPDATE od_ScopeInfo_Records SET listID=NULL;`))
	if err != nil || !reflect.DeepEqual(got.Losses, []Loss{{Code: "onedrive_sync_scope_policy_unknown", Count: 1}}) {
		t.Fatalf("incomplete scope tuple resolved policy: %v,%v", got.Losses, err)
	}
}

func TestReadIndexUnselectedGeneratedExtraRemainsUnconsumed(t *testing.T) {
	schema := strings.Replace(nativeSchema, "modifiedBy TEXT,spoCompositeID TEXT", "modifiedBy TEXT,spoCompositeID TEXT,excluded BLOB GENERATED ALWAYS AS (zeroblob(1048577)) VIRTUAL", 1)
	got, err := ReadIndex(context.Background(), fixture(t, schema, ""))
	if err != nil || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("unselected generated extra consumed: %#v,%v", got, err)
	}
}
