package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentUntilDateIncludesWholeDay(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = old })
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	for _, command := range [][]string{{"docs", "list"}, {"docs", "search", "Quarterly"}, {"search", "Quarterly", "--source", "documents"}} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			for _, bound := range []struct {
				until string
				count int
			}{{"2026-10-11", 1}, {"2026-10-10", 0}, {"2026-10-11T01:00:00Z", 0}, {"2026-10-11T01:00:01Z", 1}} {
				args := append([]string{"--no-sync"}, command...)
				code, out, stderr := e.run(append(args, "--until", bound.until)...)
				if code != 0 || len(items(t, decode(t, out))) != bound.count {
					t.Errorf("--until %s: want %d rows; exit %d %s %s", bound.until, bound.count, code, out, stderr)
				}
			}
		})
	}
}

func TestDocumentPublicRowsAndCoverageShareCurrentSnapshot(t *testing.T) {
	for _, command := range [][]string{{"docs", "list"}, {"docs", "show", "document-observation:1"}, {"search", "Quarterly", "--source", "documents"}} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			e := newEnv(t)
			seedDocumentsCLI(t, e)
			original := runDocumentsSync
			t.Cleanup(func() { runDocumentsSync = original })
			runDocumentsSync = func(ctx context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
				report, err := original(ctx, options)
				if err != nil {
					return report, err
				}
				st, err := store.Open(ctx, e.db)
				if err != nil {
					t.Fatal(err)
				}
				at := time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC)
				attempt, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{
					Kind: "office_shared", Platform: "synthetic", Locator: "/fictional/shared.json", At: at,
				}, officedocuments.Shared, "supplied")
				if err != nil {
					t.Fatal(err)
				}
				err = st.CommitDocumentObservations(ctx, attempt, store.KnowledgeOutcome{
					Status: "partial", ContentDigest: "new synthetic bytes", Qualification: store.DocumentQualification,
					At: at, Losses: map[string]int{"synthetic_partial": 1},
				}, officedocuments.Result{Documents: []officedocuments.Document{{
					Surface: officedocuments.Shared, Title: "Quarterly newer plan", URL: "https://example.invalid/newer",
					Modified: officedocuments.Timestamp{Raw: "2026-10-11T02:00:00Z", Value: at},
				}}, Losses: []officedocuments.Loss{{Code: "synthetic_partial", Count: 1}}}, store.DocumentCaptureMetadata{
					Surface: officedocuments.Shared, Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				return report, nil
			}
			code, out, stderr := e.run(append([]string{"--no-sync"}, command...)...)
			if code != 0 {
				t.Fatalf("current snapshot: %d %s %s", code, out, stderr)
			}
			coverage := decode(t, out)["source_coverage"].(map[string]any)
			source := coverage["sources"].([]any)[0].(map[string]any)
			if source["admitted_id"] != float64(2) || source["status"] != "partial" {
				t.Fatalf("current rows paired with stale source frontier: %s", out)
			}
		})
	}
}

func TestSharedSearchHelpExplainsDocumentEvidence(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("search", "--help")
	out = strings.Join(strings.Fields(out), " ") // help is wrapped: compare words
	if code != 0 || !strings.Contains(out, "docs show") || !strings.Contains(out, "unresolved documents") {
		t.Fatalf("shared search cannot explain document hit/filter: %d %s %s", code, out, stderr)
	}
}
