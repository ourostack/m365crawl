package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsCLIRefreshCannotServeBrokenSourceCoverage(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	e.exec(`alter table knowledge_locators rename column kind to broken`)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, errors.New("refresh failed")
	}
	if code, _, stderr := e.run("docs", "list"); code == 0 || !strings.Contains(stderr, "db_error") {
		t.Fatal("broken retained source coverage hidden after refresh failure")
	}
}

func TestDocumentsCLIListRejectsUnqualifiedScopeBeforeReading(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"--account", "tenant/user", "docs", "list"},
		{"docs", "list", "--source-id=-1"}, {"docs", "list", "--limit=-1"},
		{"docs", "sources", "--source-id=999"}, {"docs", "sources", "--source-id=-1"},
	} {
		if code, _, stderr := e.run(append([]string{"--max-age", "0"}, args...)...); code != 2 {
			t.Fatalf("invalid source/scope accepted: %v %s", args, stderr)
		}
	}
}
