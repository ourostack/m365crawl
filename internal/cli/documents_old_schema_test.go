package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsCLIOlderArchiveReadDoesNotMigrateOrInventFreshness(t *testing.T) {
	e := newEnv(t)
	e.sync()
	oldSync := runDocumentsSync
	acquisitions := 0
	runDocumentsSync = func(ctx context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		if options.Explicit || options.Discover || options.MaxAge != 0 ||
			options.Root != "" || options.RegistryFile != "" || options.UsageFile != "" {
			acquisitions++
			return syncer.DocumentReport{}, context.Canceled
		}
		// This facade also performs read-only registration lookup; keep that guard real.
		return oldSync(ctx, options)
	}
	t.Cleanup(func() { runDocumentsSync = oldSync })
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
		code, stdout, stderr := e.run(append([]string{"--no-sync"}, args...)...)
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
	if acquisitions != 0 {
		t.Fatal("old optional archive-only read acquired a source")
	}
	e.exec(`insert into schema_migrations(version) values(999)`)
	newerBefore, err := os.ReadFile(e.db) //nolint:gosec // G304: closed synthetic archive created under newEnv's private temp root
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"docs", "list"}, {"docs", "sources"}, {"search", "Quarterly", "--source", "documents"}} {
		code, _, stderr := e.run(append([]string{"--no-sync"}, args...)...)
		if code != errs.ExitEnvironment || errorOf(t, stderr)["code"] != errs.CodeArchiveNewer {
			t.Fatalf("future archive became successful empty output: %s", stderr)
		}
	}
	newerAfter, err := os.ReadFile(e.db) //nolint:gosec // G304: same owned synthetic archive, proving future-schema refusal is readonly
	if err != nil || !bytes.Equal(newerBefore, newerAfter) || acquisitions != 0 {
		t.Fatal("future archive refusal acquired, repaired or wrote the closed archive")
	}
}
