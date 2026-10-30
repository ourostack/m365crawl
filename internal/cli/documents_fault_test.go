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
	id := seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, os.ErrPermission
	}

	for _, format := range []string{"json", "log"} {
		code, stdout, stderr := e.run("--format", format, "--max-age", "1h", "docs", "list", "--limit", "1")
		if format == "log" {
			if !strings.HasPrefix(stdout, "docs_list=") {
				t.Fatal("degraded document log lost its operation label")
			}
			stdout = strings.TrimPrefix(stdout, "docs_list=")
		}
		if code != 0 || len(items(t, decode(t, stdout))) != 1 {
			t.Fatalf("refresh failure erased last-good row: %s", stderr)
		}
		doc := decode(t, stdout)
		row := items(t, doc)[0]
		if row["id"] != id || row["title"] != "Quarterly shared plan" {
			t.Fatal("degraded read replaced admitted document identity or content")
		}
		if age, present := doc["archive_age_seconds"]; !present || age != nil {
			t.Fatal("independent document freshness invented a Teams sync age")
		}
		coverage := doc["source_coverage"].(map[string]any)
		if coverage["refresh_error"] == nil || coverage["state"] != "registered" {
			t.Fatal("current last-good coverage hid independent refresh failure")
		}
		if format == "log" && strings.Contains(strings.TrimSpace(stdout), "\n") {
			t.Fatal("degraded document log is not one compact JSON document")
		}
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
	code, _, stderr := e.run("--no-sync", "docs", "show", id)
	if code != 2 || !strings.Contains(stderr, "document_observation_unknown") {
		t.Fatalf("lost archive became successful detail: %s", stderr)
	}
}
