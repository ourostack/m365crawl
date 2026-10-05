package calendar

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// openDB opens a fresh crawlkit store with SchemaDDL on a temp file (an in-memory database is
// per-connection, which a pool would split).
func openDB(t testing.TB) *sql.DB {
	t.Helper()
	cs, err := crawlstore.Open(context.Background(), crawlstore.Options{
		Path: filepath.Join(t.TempDir(), "cal.db"), Schema: SchemaDDL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs.DB()
}

func TestSchemaDDL(t *testing.T) {
	db := openDB(t)
	for _, table := range []string{"calendar_source_events", "calendar_sources", "calendar_matches"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s: n=%d err=%v", table, n, err)
		}
	}
	// There is no SQL view: merging happens in Go.
	var views int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='view'`).Scan(&views); err != nil || views != 0 {
		t.Fatalf("views = %d err=%v", views, err)
	}
	// Applying twice is idempotent.
	if _, err := db.Exec(SchemaDDL); err != nil {
		t.Fatal(err)
	}
	// Archive rule: removal is a sticky timestamp, so the DDL never deletes or vacuums.
	up := strings.ToUpper(SchemaDDL)
	if strings.Contains(up, "DELETE") || strings.Contains(up, "VACUUM") {
		t.Fatal("schema must not delete or vacuum")
	}
}
