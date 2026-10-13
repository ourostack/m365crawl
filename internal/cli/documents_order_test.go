package cli

import (
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

// orderEnv holds two dated Office JSON rows, three registry rows (two opened times and none) and
// one OneDrive usage source with a document row, a zone-less recent row and a collaborator.
func orderEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	seedTwoDocuments(t, e)
	seedRegistryCLI(t, e, "/fictional/registry/MicrosoftRegistrationDB_order.reg",
		registryCLIObservation(1, "Opened earlier", "2026-10-07T09:00:00Z"), registryCLIObservation(2, "Opened later", "2026-10-09T09:00:00+02:00"),
		registryCLIObservation(3, "Opened unknown", "last week"))
	recent := usageCLIRecent("native-order")
	recent.FileModifiedRaw, recent.LastAccessedRaw = "2026-10-09 12:34:56", "2026-10-11 11:00:00"
	document := usageCLIDocument("native-order", "Quarterly plan usage")
	document.ModifiedRaw = "2026-10-11T01:30:00Z"
	seedUsageCLI(t, e, "/fictional/Business_order/Microsoft.FileUsageSync.db", onedriveusage.Result{
		Recent: []onedriveusage.RecentObservation{recent}, Documents: []onedriveusage.DocumentObservation{document},
		Collaborators: []onedriveusage.Collaborator{{ID: "person-order", DisplayName: "Synthetic Collaborator"}}})
	return e
}

func titlesOf(t *testing.T, doc map[string]any) (out []string) {
	t.Helper()
	for _, row := range items(t, doc) {
		title, _ := row["title"].(string)
		out = append(out, title)
	}
	return out
}

// docs list --order-by opened_at puts the rows that state an opened time first, newest first, so
// `--application registry --limit N` is the N most recently opened registry documents. The
// default order is unchanged, and a text list whose rows all lack a modification time says so.
func TestDocsListOrderByOpened(t *testing.T) {
	e := orderEnv(t)
	doc, _ := usageCLIRun(t, e, "docs", "list", "--application", "registry", "--order-by", "opened_at", "--limit", "2")
	if got := titlesOf(t, doc); len(got) != 2 || got[0] != "Opened later" || got[1] != "Opened earlier" || doc["truncated"] != true {
		t.Fatalf("most recently opened registry rows = %q", got)
	}
	byDefault, _ := usageCLIRun(t, e, "docs", "list", "--application", "registry")
	explicit, _ := usageCLIRun(t, e, "docs", "list", "--application", "registry", "--order-by", "modified_at")
	if got := titlesOf(t, byDefault); len(got) != 3 || got[0] != "Opened earlier" || strings.Join(got, "|") != strings.Join(titlesOf(t, explicit), "|") {
		t.Fatalf("default order = %q", got)
	}
	all, _ := usageCLIRun(t, e, "docs", "list", "--order-by", "opened_at")
	if got := titlesOf(t, all); len(got) != 8 || got[0] != "Opened later" || got[1] != "Opened earlier" || got[2] != "Quarterly plan two" {
		t.Fatalf("opened order over every row = %q", got)
	}
	for _, args := range [][]string{{"docs", "list", "--order-by", "captured_at"}, {"docs", "search", "plan", "--order-by", "opened_at"}} {
		if code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...); code != 2 || stdout != "" || !strings.Contains(stderr, "order-by") {
			t.Fatalf("%v: exit %d %s", args, code, stderr)
		}
	}
	hint := documentOrderHint
	text := usageCLIText(t, e, "--format", "text", "docs", "list", "--application", "registry")
	if strings.Count(text, hint) != 1 {
		t.Fatalf("a list of undated rows does not name --order-by once:\n%s", text)
	}
	for _, args := range [][]string{{"docs", "list", "--application", "registry", "--order-by", "opened_at"}, {"docs", "list"},
		{"docs", "list", "--application", "onedrive", "--query", "nothingmatches"}} {
		if text := usageCLIText(t, e, append([]string{"--format", "text"}, args...)...); strings.Contains(text, "--order-by") {
			t.Fatalf("%v prints the order hint although it does not apply:\n%s", args, text)
		}
	}
	help := helpOf(t, "docs", "list")
	for _, want := range []string{"--order-by", "m365crawl docs list --application registry --order-by opened_at --limit 5", "OneDrive usage rows state no opened time"} {
		if !strings.Contains(strings.Join(strings.Fields(help), " "), want) {
			t.Fatalf("docs list --help lacks %q", want)
		}
	}
	if strings.Contains(helpOf(t, "docs", "search"), "--order-by") {
		t.Fatal("docs search --help names --order-by, which it does not accept")
	}
}

// A date window silently dropped every row without a modification time. The result now counts
// them by kind and says why, in docs list, docs search and shared search, and only then.
func TestDocsDateWindowCountsUndatedRows(t *testing.T) {
	e := orderEnv(t)
	notices := func(doc map[string]any) string {
		parts := []string{}
		list, _ := doc["notices"].([]any)
		for _, notice := range list {
			parts = append(parts, notice.(string))
		}
		return strings.Join(parts, "\n")
	}
	want := "--since/--until left out 5 rows that otherwise matched because they have no modification time: 3 Office registry rows (the registry states none), " +
		"1 OneDrive usage recent row (its clock has no zone or is missing, so it is not placed in time) and 1 OneDrive usage collaborator row (a profile has none). Drop the date filter to list them"
	for _, args := range [][]string{{"docs", "list", "--since", "2026-10-01"}, {"docs", "search", "--since", "2026-10-01"}} {
		doc, _ := usageCLIRun(t, e, args...)
		if got := notices(doc); len(items(t, doc)) != 3 || !strings.Contains(got, want) {
			t.Fatalf("%v: %d rows, notices %q", args, len(items(t, doc)), got)
		}
		if text := usageCLIText(t, e, append([]string{"--format", "text"}, args...)...); !strings.Contains(text, want) {
			t.Fatalf("%v text lacks the date-window notice:\n%s", args, text)
		}
	}
	narrowed, _ := usageCLIRun(t, e, "docs", "list", "--until", "2026-10-01", "--application", "registry", "--query", "later")
	if got := notices(narrowed); len(items(t, narrowed)) != 0 || !strings.Contains(got, "left out 1 row that otherwise matched because it has no modification time: 1 Office registry row (the registry states none). Drop the date filter to list it") {
		t.Fatalf("narrowed window notices %q", got)
	}
	shared, _ := usageCLIRun(t, e, "search", "Opened", "--since", "2026-10-01", "--source", "documents")
	if got := notices(shared); len(items(t, shared)) != 0 || !strings.Contains(got, "left out 3 rows") || !strings.Contains(got, "3 Office registry rows") {
		t.Fatalf("shared search notices %q", got)
	}
	for _, args := range [][]string{{"docs", "list"}, {"docs", "list", "--since", "2026-10-01", "--application", "supplied"}, {"search", "Opened", "--source", "documents"}} {
		if doc, _ := usageCLIRun(t, e, args...); strings.Contains(notices(doc), "left out") {
			t.Fatalf("%v reports rows left out of a date window: %q", args, notices(doc))
		}
	}
	// The flag's own help says which rows it never matches; docs --help says why for OneDrive.
	for _, command := range [][]string{{"docs", "list"}, {"docs", "search"}} {
		help := strings.Join(strings.Fields(helpOf(t, command...)), " ")
		if !strings.Contains(help, "Never matches a registry row, a OneDrive usage recent row or a collaborator row") {
			t.Fatalf("%v --since/--until help does not say which rows never match:\n%s", command, help)
		}
	}
	if help := strings.Join(strings.Fields(helpOf(t, "docs")), " "); !strings.Contains(help, "every clock of a recent row is written without a zone") {
		t.Fatal("docs --help does not state that recent-row clocks carry no zone")
	}
}

// Usage hits take their place in a shared search by their own modification time.
func TestUsageHitsMergeByModificationTime(t *testing.T) {
	e := orderEnv(t)
	doc, _ := usageCLIRun(t, e, "search", "Quarterly")
	if got := titlesOf(t, doc); len(got) != 3 || got[0] != "Quarterly plan two" || got[1] != "Quarterly plan usage" || got[2] != "Quarterly plan one" {
		t.Fatalf("merged order = %q", got)
	}
}

// A text table gives a usage row's type its own short column and keeps the title column for what
// the file states, so rows stay distinguishable when the title is clipped.
func TestUsageTextRowsKeepTheTitleColumn(t *testing.T) {
	e := orderEnv(t)
	t.Setenv("COLUMNS", "100")
	text := usageCLIText(t, e, "--format", "text", "docs", "list", "--application", "onedrive")
	header, _, _ := strings.Cut(text, "\n")
	if !strings.Contains(header, "usage_type") || strings.Contains(text, "(recent row)") || strings.Contains(text, "(collaborator)") {
		t.Fatalf("usage text list:\n%s", text)
	}
	for _, want := range [][]string{{"recent", "native-order"}, {"collaborator", "Synthetic Collaborator"}, {"document", "Quarterly plan usage"}} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			found = found || (slicesContain(fields, want[0]) && strings.Contains(line, want[1][:10]))
		}
		if !found {
			t.Fatalf("no text row with usage type %q and stated value %q:\n%s", want[0], want[1], text)
		}
	}
	if other := usageCLIText(t, e, "--format", "text", "docs", "list", "--application", "registry"); !strings.Contains(strings.SplitN(other, "\n", 2)[0], "usage_type") {
		t.Fatalf("the column is not stable across row kinds:\n%s", other)
	}
	search := usageCLIText(t, e, "--format", "text", "search", "native-order")
	if !strings.Contains(search, "recent row; ") || !strings.Contains(search, "document row; ") || strings.Contains(search, "(recent row)") {
		t.Fatalf("shared search text for a usage recent row:\n%s", search)
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A file given to the wrong flag fails as unrecognized; its fix names the flag for the other kind
// of file. A database in write-ahead mode is told that a retry cannot pass.
func TestWrongFamilyAndWriteAheadRemedies(t *testing.T) {
	for reason, want := range map[string]string{
		"office_registry_index_unrecognized": "If it is a OneDrive usage file, register it with m365crawl docs sync --onedrive-usage-file FILE instead",
		"onedrive_usage_index_unrecognized":  "If it is an Office registry file, register it with m365crawl docs sync --registry-file FILE instead",
		"onedrive_usage_input_unqualified":   "A database in write-ahead mode stays refused however often the sync is retried",
		"office_registry_input_unqualified":  "A database in write-ahead mode stays refused however often the sync is retried",
	} {
		if fix := documentFailureFix[reason]; !strings.Contains(fix, want) {
			t.Fatalf("%s fix lacks %q: %s", reason, want, fix)
		}
	}
	for _, reason := range []string{"office_registry_index_unrecognized", "onedrive_usage_index_unrecognized"} {
		if !strings.Contains(documentFailureFix[reason], "this source stays listed as failed") {
			t.Fatalf("%s fix does not say the wrong-flag source stays listed", reason)
		}
	}
}
