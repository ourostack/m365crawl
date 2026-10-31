package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/schemaguard"
)

// Every <Feature>SchemaVersion constant is in schemaFeatures with its number, no two share one,
// none is above SchemaVersion, and a fresh archive has every registered table (#306).
func TestSchemaFeatureNumbers(t *testing.T) {
	files := map[string][]byte{}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name) //nolint:gosec // G304: this package's own source
		if err != nil {
			t.Fatal(err)
		}
		files[name] = b
	}
	if problems := schemaguard.Check(nil, files); len(problems) != 0 {
		t.Fatalf("schema numbers: %v", problems)
	}
	numbers, _ := schemaguard.Versions(files)
	registered := map[string]int{}
	for _, f := range schemaFeatures {
		registered[f.constant] = f.version
		if len(f.tables) == 0 || f.name == "" {
			t.Errorf("%s: a feature names itself and its tables", f.constant)
		}
	}
	if numbers.Schema != SchemaVersion || len(registered) != len(schemaFeatures) || len(registered) != len(numbers.Features) {
		t.Fatalf("registered %v, source %+v", registered, numbers)
	}
	for name, v := range numbers.Features {
		if registered[name] != v {
			t.Errorf("%s = %d is not registered in schemaFeatures with that number", name, v)
		}
	}
	s := openTest(t)
	for _, f := range schemaFeatures {
		for _, table := range f.tables {
			var n int
			query, args := `select count(*) from sqlite_master where type='table' and name=?`, []any{table}
			if name, column, ok := strings.Cut(table, "."); ok {
				query, args = `select count(*) from pragma_table_info(?) where name=?`, []any{name, column}
			}
			if err := s.db.QueryRow(query, args...).Scan(&n); err != nil || n != 1 {
				t.Errorf("%s: a fresh archive has no table %s (%v)", f.name, table, err)
			}
		}
	}
}
