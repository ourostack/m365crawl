package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsCLIUnqualifiedInputGetsSourceRemedyNotArchiveRepair(t *testing.T) {
	e := newEnv(t)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, &officecapture.ReadError{Code: "office_json_input_unqualified"}
	}
	code, _, stderr := e.run("docs", "sync", "--root", "/synthetic")
	if code != 3 || !strings.Contains(stderr, "office_json_input_unqualified") ||
		!strings.Contains(stderr, "ordinary") || strings.Contains(stderr, `"code":"db_error"`) {
		t.Fatalf("input refusal mislabeled archive corruption: %s", stderr)
	}
}

func TestDocumentsCLIRefreshFailureServesCurrentLastGoodCoverage(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, os.ErrPermission
	}

	code, stdout, stderr := e.run("--max-age", "1h", "docs", "list", "--limit", "1")
	if code != 0 || len(items(t, decode(t, stdout))) != 1 {
		t.Fatalf("refresh failure erased last-good row: %s", stderr)
	}
	coverage := decode(t, stdout)["source_coverage"].(map[string]any)
	if coverage["refresh_error"] == nil || coverage["state"] != "registered" {
		t.Fatal("current last-good coverage hid independent refresh failure")
	}
}

func TestDocumentsCLIShowArchiveRemovedBetweenLookupAndRefreshFailsWithoutPanic(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		if err := os.Rename(e.db, e.db+".removed"); err != nil {
			t.Fatal(err)
		}
		return syncer.DocumentReport{}, nil
	}
	code, _, stderr := e.run("--max-age", "0", "docs", "show", id)
	if code != 2 || !strings.Contains(stderr, "document_observation_unknown") {
		t.Fatalf("lost archive became successful detail: %s", stderr)
	}
}
