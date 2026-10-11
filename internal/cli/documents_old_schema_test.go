package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestDocumentsCLIOlderArchiveReadDoesNotMigrateOrInventFreshness(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.exec(`drop table document_capture_members;
drop table document_capture_metadata;
drop table document_observations;
drop table document_locator_metadata;
drop table document_fts;
delete from schema_migrations where version=11`)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"docs", "list"}, {"docs", "sources"}, {"search", "Quarterly", "--source", "documents"}} {
		code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 || strings.Contains(stdout, "needs_upgrade") || strings.Contains(stdout, "needs_sync") ||
			len(items(t, decode(t, stdout))) != 0 {
			t.Fatalf("old optional archive touched legacy freshness or became a capture: %s", stderr)
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(e.db)
	if err != nil || !bytes.Equal(before, after) || !current.ModTime().Equal(info.ModTime()) {
		t.Fatal("old optional archive was migrated/written by max-age0 read")
	}
}
