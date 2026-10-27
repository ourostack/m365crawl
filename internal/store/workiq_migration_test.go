package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	crawlstore "github.com/openclaw/crawlkit/store"
)

func TestWorkIQSettingsAndScope(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if enabled, err := s.WorkIQEnabled(ctx); err != nil || enabled {
		t.Fatal("new archive unexpectedly enabled WorkIQ")
	}
	for _, enabled := range []bool{true, true, false, false} {
		if err := s.SetWorkIQEnabled(ctx, enabled); err != nil {
			t.Fatal(err)
		}
		if got, err := s.WorkIQEnabled(ctx); err != nil || got != enabled {
			t.Fatalf("opt-in did not persist: %v %v", got, err)
		}
	}
	if got := rowCount(t, s, `select count(*) from sync_runs`); got != 0 {
		t.Fatal("configuration fabricated source attempts")
	}
}

func TestWorkIQMigrationPreservesCacheArchive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	legacyDDL := strings.Replace(schemaDDL, workiqSchemaDDL, "", 1)
	cs, err := crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: legacyDDL, SchemaVersion: 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cs.DB().ExecContext(ctx, `insert into meta(key,value) values('historical-clock','2026-09-01T12:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	must0(cs.Close())
	old := must(OpenReadOnly(ctx, path))
	if enabled, err := old.WorkIQEnabled(ctx); err != nil || enabled {
		t.Fatalf("old read-only source state: %v %v", enabled, err)
	}
	must0(old.Close())
	s := must(Open(ctx, path))
	defer func() { _ = s.Close() }()
	if got := rowCount(t, s, `select max(version) from schema_migrations`); got != 21 {
		t.Fatalf("migration version=%d, want21", got)
	}
	var oldClock string
	must0(s.db.QueryRowContext(ctx, `select value from meta where key='historical-clock'`).Scan(&oldClock))
	if oldClock != "2026-09-01T12:00:00.000Z" || rowCount(t, s, `select count(*) from workiq_attempts`) != 0 {
		t.Fatal("migration fabricated source observations")
	}
}
