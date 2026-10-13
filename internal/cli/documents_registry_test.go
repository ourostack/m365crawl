package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

var registryCLIAt = time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC)

// seedRegistryCLI admits one synthetic registry capture and returns its source's first observation ID.
func seedRegistryCLI(t *testing.T, e *env, locator string, documents ...officeregistry.Observation) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	attempt, err := st.BeginRegistryDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.RegistryDocumentKind,
		Platform: "synthetic", Locator: locator, At: registryCLIAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) == 0 {
		err = st.RecordKnowledgeFailure(ctx, attempt, store.KnowledgeOutcome{Status: "failed",
			Reason: "office_registry_index_unrecognized", At: registryCLIAt})
	} else {
		err = st.CommitRegistryDocumentObservations(ctx, attempt, store.KnowledgeOutcome{Status: "complete",
			ContentDigest: "synthetic-registry", Qualification: store.RegistryDocumentQualification, At: registryCLIAt},
			officeregistry.Result{Documents: documents}, store.RegistryCaptureMetadata{AcquiredAt: &registryCLIAt, SourceObservedAt: &registryCLIAt})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func registryCLIObservation(node int64, title, opened string) officeregistry.Observation {
	size, pinned := int64(42), true
	return officeregistry.Observation{NodeID: node, ApplicationPath: "Word", ApplicationValue: "synthetic application value",
		Title: title, URL: "file:///synthetic/registry/" + title + ".docx", FriendlyPath: "Synthetic folder > " + title,
		OpenedRaw: opened, Size: &size, Pinned: &pinned}
}

func coverageSources(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	coverage, _ := doc["source_coverage"].(map[string]any)
	raw, _ := coverage["sources"].([]any)
	out := []map[string]any{}
	for _, source := range raw {
		out = append(out, source.(map[string]any))
	}
	return out
}

func TestRegistryUnadmittedRetryRoute(t *testing.T) {
	e := newEnv(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "MicrosoftRegistrationDB_synthetic copy.reg")
	if err := os.WriteFile(file, []byte(strings.Repeat("not a database ", 16)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run("docs", "sync", "--registry-file", file)
	if code != 0 {
		t.Fatalf("a refused registry file is a source outcome, not a command error: %d %s", code, stderr)
	}
	doc := decode(t, stdout)
	sources := coverageSources(t, doc)
	if doc["failed"] != float64(1) || len(sources) != 1 || sources[0]["source_id"] != nil || sources[0]["kind"] != store.RegistryDocumentKind ||
		!strings.HasPrefix(sources[0]["reason"].(string), "office_registry_") {
		t.Fatalf("first failed registry capture: %s", stdout)
	}
	retry := "m365crawl docs sync --registry-file " + shellArg(file)
	fix, _ := sources[0]["fix"].(string)
	if !strings.Contains(fix, retry) || strings.Contains(stdout, "--source-id 0") || strings.Contains(stdout, "--source-id null") {
		t.Fatalf("unadmitted registry source has no usable explicit retry route: %q", fix)
	}
	for _, args := range [][]string{{"docs", "sources"}, {"--max-age", "0", "docs", "list"}, {"--format", "text", "docs", "sources"},
		{"--max-age", "0", "search", "anything", "--source", "documents"}} {
		code, stdout, stderr := e.run(args...)
		if code != 0 || !strings.Contains(stdout, "--registry-file") || strings.Contains(stdout, "--source-id 0") {
			t.Fatalf("%v lost the retry route: %d %s %s", args, code, stdout, stderr)
		}
	}
	// Once a source is admitted, a later failure names its real source ID.
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_admitted.reg", registryCLIObservation(12, "Admitted", ""))
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := st.BeginRegistryDocumentAttempt(context.Background(), store.KnowledgeInput{Kind: store.RegistryDocumentKind,
		Platform: "synthetic", Locator: "/fictional/MicrosoftRegistrationDB_admitted.reg", At: registryCLIAt.Add(time.Minute)})
	if err == nil {
		err = st.RecordKnowledgeFailure(context.Background(), attempt, store.KnowledgeOutcome{Status: "failed",
			Reason: "office_registry_source_busy", At: registryCLIAt.Add(time.Minute)})
	}
	if closeErr := st.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	_, stdout, _ = e.run("--max-age", "0", "docs", "sources")
	admitted := false
	for _, source := range coverageSources(t, decode(t, stdout)) {
		if id, ok := source["source_id"].(float64); ok {
			admitted = true
			fix, _ := source["fix"].(string)
			if !strings.Contains(fix, "m365crawl docs sync --source-id "+strconv.FormatInt(int64(id), 10)) ||
				strings.Contains(fix, "--registry-file") || !strings.Contains(source["effect"].(string), "last good capture") {
				t.Fatalf("admitted registry failure route: %v", source)
			}
		}
	}
	if !admitted {
		t.Fatal("admitted registry source missing")
	}
}

func TestRegistrySyncFlagsAndDiscoveryOutcomes(t *testing.T) {
	e := newEnv(t)
	abs := filepath.Join(t.TempDir(), "MicrosoftRegistrationDB_synthetic.reg")
	for _, args := range [][]string{
		{"docs", "sync", "--registry-file", abs, "--root", filepath.Dir(abs)},
		{"docs", "sync", "--registry-file", abs, "--source-id", "1"},
		{"docs", "sync", "--registry-file", "relative.reg"},
		{"docs", "sync", "--registry-file", abs + string(filepath.Separator) + ".." + string(filepath.Separator) + "other.reg"},
	} {
		if code, _, stderr := e.run(args...); code != 2 || !strings.Contains(stderr, "--registry-file") {
			t.Fatalf("invalid selection accepted: %v %d %s", args, code, stderr)
		}
	}
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	var got syncer.DocumentOptions
	runDocumentsSync = func(_ context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		got = options
		return syncer.DocumentReport{Discovery: []syncer.DocumentDiscovery{
			{Kind: syncer.DocumentFamilyJSON, Code: "office_json_input_unqualified"},
			{Kind: store.RegistryDocumentKind, Code: "office_registry_unavailable"},
			{Kind: store.RegistryDocumentKind, Code: "office_registry_future_code"}}}, nil
	}
	code, stdout, stderr := e.run("docs", "sync")
	if code != 0 || got.RegistryFile != "" || got.Root != "" || !got.Discover || !got.Explicit {
		t.Fatalf("default sync: %d %+v %s", code, got, stderr)
	}
	discovery, _ := decode(t, stdout)["discovery"].([]any)
	if len(discovery) != 3 {
		t.Fatalf("family discovery outcomes missing: %s", stdout)
	}
	for _, raw := range discovery {
		outcome := raw.(map[string]any)
		if outcome["kind"] == "" || outcome["code"] == "" || outcome["meaning"] == "" || outcome["source_id"] != nil {
			t.Fatalf("discovery outcome is not a fixed sourceless family outcome: %v", outcome)
		}
	}
	if !strings.Contains(stdout, "docs sync --registry-file FILE") || !strings.Contains(stdout, "docs sync --root DIR") {
		t.Fatalf("discovery outcomes do not name the supplied-input routes: %s", stdout)
	}
	code, stdout, _ = e.run("--format", "text", "docs", "sync")
	if code != 0 || !strings.Contains(stdout, "discovery office_registry: office_registry_unavailable") ||
		!strings.Contains(stdout, "discovery office_json: office_json_input_unqualified") {
		t.Fatalf("text discovery outcomes: %s", stdout)
	}
	if code, _, _ := e.run("docs", "sync", "--registry-file", abs); code != 0 || got.RegistryFile != abs || !got.Discover {
		t.Fatalf("registry file not routed: %+v", got)
	}
}

func TestRegistryZeroHitCoverageAfterGlobalLimit(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_good.reg", registryCLIObservation(12, "Quarterly registry plan", "2026-09-15T12:00:00Z"))
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_failed.reg")
	check := func(args []string, wantItems int, wantTruncated bool) map[string]any {
		t.Helper()
		code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
		doc := decode(t, stdout)
		rows := items(t, doc)
		for _, row := range rows {
			if row["variant"] != nil || row["registry_native"] != nil {
				t.Fatalf("%v: a registry row survived the clip or filter: %v", args, row)
			}
		}
		if len(rows) != wantItems || doc["truncated"] != wantTruncated {
			t.Fatalf("%v: items %d truncated %v", args, len(rows), doc["truncated"])
		}
		kinds, failed := 0, false
		for _, source := range coverageSources(t, doc) {
			if source["kind"] == store.RegistryDocumentKind {
				kinds++
				if source["status"] == "failed" {
					fix, _ := source["fix"].(string)
					failed = source["reason"] == "office_registry_index_unrecognized" && strings.Contains(fix, "--registry-file")
				}
			}
		}
		if kinds != 2 || !failed {
			t.Fatalf("%v: registry coverage or failure hidden behind zero registry hits: %s", args, stdout)
		}
		return doc
	}
	doc := check([]string{"docs", "list", "--limit", "1"}, 1, true)
	if scope := doc["source_coverage"].(map[string]any)["scope"]; scope != "office_document_metadata" {
		t.Fatalf("coverage scope %v", scope)
	}
	check([]string{"docs", "search", "Quarterly", "--limit", "1"}, 1, true)
	check([]string{"search", "Quarterly", "--source", "documents", "--limit", "1"}, 1, true)
	// Unknown modified time never matches a bounded window, and opened time never stands in for it.
	check([]string{"docs", "list", "--since", "2026-10-01"}, 1, false)
	check([]string{"docs", "list", "--since", "2026-09-14", "--until", "2026-09-16"}, 0, false)
	check([]string{"--fields", "id", "docs", "list", "--surface", "shared"}, 1, false)
	code, stdout, _ := e.run("--max-age", "0", "docs", "list", "--application", "REGISTRY")
	rows := items(t, decode(t, stdout))
	if code != 0 || len(rows) != 1 || rows[0]["variant"] != store.RegistryDocumentKind || rows[0]["modified_at"] != nil {
		t.Fatalf("--application registry: %s", stdout)
	}
}

func TestRegistryCLIClocksAndNoReferencedIO(t *testing.T) {
	e := newEnv(t)
	referenced := filepath.Join(t.TempDir(), "referenced.docx")
	if err := os.WriteFile(referenced, []byte("synthetic referenced bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(referenced)
	if err != nil {
		t.Fatal(err)
	}
	valid := registryCLIObservation(12, "Valid opened time", "2026-10-09T14:34:56.5+02:00")
	invalid := registryCLIObservation(13, "Unparsed opened time", "yesterday afternoon")
	invalid.FriendlyPath, invalid.URL, invalid.Size, invalid.Pinned = referenced, "file://"+filepath.ToSlash(referenced), nil, nil
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_clocks.reg", valid, invalid)
	seedDocumentsCLI(t, e)
	code, stdout, stderr := e.run("--max-age", "0", "docs", "list", "--application", "registry")
	if code != 0 {
		t.Fatal(stderr)
	}
	byTitle := map[string]map[string]any{}
	for _, row := range items(t, decode(t, stdout)) {
		byTitle[row["title"].(string)] = row
	}
	good, bad := byTitle["Valid opened time"], byTitle["Unparsed opened time"]
	if good == nil || bad == nil {
		t.Fatalf("registry rows missing: %s", stdout)
	}
	for _, row := range []map[string]any{good, bad} {
		_, native := row["native"]
		if row["variant"] != "office_registry" || row["application"] != "registry" || row["surface"] != "" || native ||
			row["modified_at"] != nil || row["text_state"] != "not_captured" || row["association_state"] != "unresolved" ||
			row["captured_at"] != "2026-10-11T02:00:00Z" {
			t.Fatalf("registry row shape: %v", row)
		}
	}
	if good["opened_at"] != "2026-10-09T12:34:56.5Z" || good["registry_native"].(map[string]any)["OpenedRaw"] != "2026-10-09T14:34:56.5+02:00" ||
		good["registry_native"].(map[string]any)["Size"] != float64(42) || good["registry_native"].(map[string]any)["Pinned"] != true {
		t.Fatalf("parsed opened time or native claims: %v", good)
	}
	opened, present := bad["opened_at"]
	native := bad["registry_native"].(map[string]any)
	if !present || opened != nil || native["OpenedRaw"] != "yesterday afternoon" || native["Size"] != nil || native["Pinned"] != nil ||
		native["FriendlyPath"] != referenced {
		t.Fatalf("an unparsed opened time must stay raw with a visible null, never a fabricated date: %v", bad)
	}
	// A JSON row keeps its exact legacy keys.
	_, stdout, _ = e.run("--max-age", "0", "docs", "list", "--surface", "shared")
	legacy := items(t, decode(t, stdout))[0]
	for _, key := range []string{"variant", "registry_native", "opened_at"} {
		if _, present := legacy[key]; present {
			t.Fatalf("Office JSON row gained %s", key)
		}
	}
	if legacy["native"] == nil {
		t.Fatal("Office JSON row lost its native claims")
	}
	// Descriptive strings obey --max-text; identifiers and raw clocks stay exact.
	_, stdout, _ = e.run("--max-age", "0", "--max-text", "5", "docs", "list", "--application", "registry", "--limit", "1")
	clipped := items(t, decode(t, stdout))[0]
	clippedNative := clipped["registry_native"].(map[string]any)
	if clipped["text_truncated"] != true || len([]rune(clipped["title"].(string))) > 6 || clipped["title"] != clippedNative["Title"] ||
		len([]rune(clippedNative["FriendlyPath"].(string))) > 6 || !strings.HasPrefix(clipped["url"].(string), "file://") || clipped["url"] != clippedNative["URL"] {
		t.Fatalf("max-text clipping: %v", clipped)
	}
	id := bad["id"].(string)
	show := func(format string) string {
		t.Helper()
		code, stdout, stderr := e.run("--max-age", "0", "--format", format, "docs", "show", id)
		if code != 0 {
			t.Fatal(stderr)
		}
		return stdout
	}
	jsonBefore, textBefore := show("json"), show("text")
	detail := items(t, decode(t, jsonBefore))[0]
	if detail["references_total"] != float64(2) || detail["opened_at"] != nil || detail["variant"] != "office_registry" {
		t.Fatalf("registry detail: %s", jsonBefore)
	}
	for _, want := range []string{id, "registry native claims", "yesterday afternoon", "opened_at: unknown", "modified_at: unknown", "not_captured", "never opened"} {
		if !strings.Contains(textBefore, want) {
			t.Fatalf("registry text detail lacks %q:\n%s", want, textBefore)
		}
	}
	if text := show("text"); strings.Contains(text, "surface :") || strings.Contains(text, "registry/;") {
		t.Fatalf("registry detail rendered an invented surface: %s", text)
	}
	code, stdout, stderr = e.run("--max-age", "0", "--format", "text", "docs", "show", good["id"].(string))
	if code != 0 || !strings.Contains(stdout, "opened_at: 2026-10-09T12:34:56.5Z") {
		t.Fatalf("parsed opened time missing from text detail: %s %s", stdout, stderr)
	}
	after, err := os.Stat(referenced)
	content, readErr := os.ReadFile(referenced) //nolint:gosec // G304: the test's own temporary file
	if err != nil || readErr != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() || string(content) != "synthetic referenced bytes" {
		t.Fatal("a referenced document changed")
	}
	// The output cannot depend on the referenced document: it is identical once that file is gone.
	if err := os.Rename(referenced, referenced+".moved"); err != nil {
		t.Fatal(err)
	}
	if show("json") != jsonBefore || show("text") != textBefore {
		t.Fatal("document output depends on the referenced file")
	}
}

func TestRegistryReadOnlyRepeatLeavesArchiveBytes(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_repeat.reg", registryCLIObservation(12, "Repeat", "not a clock"))
	seedRegistryCLI(t, e, "/fictional/MicrosoftRegistrationDB_failed.reg")
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(ctx context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		if options.Discover || options.Explicit || options.RegistryFile != "" || options.Root != "" {
			t.Errorf("an ordinary read discovered or forced capture: %+v", options)
		}
		return run(ctx, options)
	}
	archive := func() []byte {
		t.Helper()
		raw, err := os.ReadFile(e.db)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := archive()
	for _, args := range [][]string{{"docs", "list"}, {"docs", "search", "Repeat"}, {"docs", "show", "document-observation:1"},
		{"docs", "sources"}, {"search", "Repeat", "--source", "documents"}, {"--format", "text", "docs", "list"}} {
		args = append([]string{"--max-age", "0"}, args...)
		code, first, stderr := e.run(args...)
		_, second, _ := e.run(args...)
		if code != 0 || first != second {
			t.Fatalf("%v is not a repeatable read: %d %s", args, code, stderr)
		}
	}
	if !bytes.Equal(before, archive()) {
		t.Fatal("archive-only document reads wrote the archive")
	}
}

func TestRegistryCoverageNoticesAndRetryQuoting(t *testing.T) {
	coverage := documentsCoverage([]store.KnowledgeSource{
		{ID: 1, AdmittedID: 1, Kind: store.RegistryDocumentKind, Status: "partial", Losses: map[string]int{"office_registry_document_unmapped": 2}},
		{ID: 2, AdmittedID: 2, Kind: store.RegistryDocumentKind, Status: "complete", Reason: syncer.RegistryListedNoDocuments},
		{Kind: store.RegistryDocumentKind, Status: "failed", Reason: "office_registry_future_reason", Locator: `C:\synthetic\copy one.reg`},
		{Kind: store.RegistryDocumentKind, Status: "cancelled", Reason: "office_registry_cancelled", Locator: "/synthetic/it's.reg"},
	})
	if len(coverage.Notices) != 2 || !strings.HasPrefix(coverage.Notices[0], syncer.RegistryListedNoDocuments) ||
		!strings.HasPrefix(coverage.Notices[1], "office_registry_document_unmapped") || coverage.Sources[0].Fix != "" {
		t.Fatalf("registry notices: %+v", coverage.Notices)
	}
	if fix := coverage.Sources[2].Fix; !strings.Contains(fix, `--registry-file "C:\synthetic\copy one.reg"`) || !strings.Contains(fix, "private copy") {
		t.Fatalf("unknown registry reason or Windows path retry: %s", fix)
	}
	if fix := coverage.Sources[3].Fix; !strings.Contains(fix, `--registry-file '/synthetic/it'\''s.reg'`) {
		t.Fatalf("quoted retry: %s", fix)
	}
}
