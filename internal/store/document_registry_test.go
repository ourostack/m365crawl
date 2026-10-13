package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
)

func registryObservation(node int64, title string) officeregistry.Observation {
	size, pinned := int64(42), true
	return officeregistry.Observation{NodeID: node, ApplicationPath: "Word", ApplicationValue: "synthetic application value",
		Title: title, URL: "file:///synthetic/registry-document.docx", FriendlyPath: "Synthetic friendly path",
		OpenedRaw: "2026-10-09T14:34:56.5+02:00", Size: &size, Pinned: &pinned}
}

func beginRegistry(t *testing.T, s *Store, locator string, at time.Time) KnowledgeAttempt {
	t.Helper()
	attempt, err := s.BeginRegistryDocumentAttempt(context.Background(), KnowledgeInput{
		Kind: RegistryDocumentKind, Platform: "synthetic", Locator: locator, At: at})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func admitRegistry(t *testing.T, s *Store, attempt KnowledgeAttempt, at time.Time, status, digest string, result officeregistry.Result) {
	t.Helper()
	if err := s.CommitRegistryDocumentObservations(context.Background(), attempt, KnowledgeOutcome{
		Status: status, ContentDigest: digest, Qualification: RegistryDocumentQualification, At: at.Add(time.Second),
		SourceObservedAt: at}, result, RegistryCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

func registryRows(t *testing.T, s *Store, filter DocumentFilter) []DocumentObservationRow {
	t.Helper()
	rows, _, err := s.DocumentObservations(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRegistryDocumentNativePayload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	full := registryObservation(12, "Synthetic registry title")
	bare := officeregistry.Observation{NodeID: 13, ApplicationPath: "Excel", ApplicationValue: "",
		Title: "Bare registry row", URL: "file:///synthetic/registry-document.docx", OpenedRaw: "not a clock"}
	// The same node, path and URL under another source and another app claim stay independent.
	other := registryObservation(12, "Synthetic registry title")
	other.ApplicationPath, other.ApplicationValue = "PowerPoint", "Word"
	a := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_a.reg", base)
	admitRegistry(t, s, a, base, "complete", "a-one", officeregistry.Result{Documents: []officeregistry.Observation{full, bare}})
	b := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_b.reg", base)
	admitRegistry(t, s, b, base, "partial", "b-one", officeregistry.Result{Documents: []officeregistry.Observation{other},
		Losses: []officeregistry.Loss{{Code: "office_registry_document_unmapped", Count: 2}, {Code: "office_registry_ancestry_unmapped", Count: 1}}})
	rows := registryRows(t, s, DocumentFilter{})
	if len(rows) != 3 {
		t.Fatalf("independent native claims collapsed: %d rows", len(rows))
	}
	byTitle := map[string][]DocumentObservationRow{}
	for _, row := range rows {
		if row.Variant != RegistryDocumentKind || row.Registry == nil || row.Application != "registry" ||
			!reflect.DeepEqual(row.Document, officedocuments.Document{}) {
			t.Fatalf("registry row not a concrete native variant: %+v", row)
		}
		byTitle[row.Registry.Title] = append(byTitle[row.Registry.Title], row)
	}
	var first, second DocumentObservationRow
	for _, row := range byTitle["Synthetic registry title"] {
		if row.SourceID == byTitle["Bare registry row"][0].SourceID {
			first = row
		} else {
			second = row
		}
	}
	if !reflect.DeepEqual(*first.Registry, full) || !reflect.DeepEqual(*second.Registry, other) {
		t.Fatal("complete native payload not preserved per source")
	}
	opened := time.Date(2026, 10, 9, 12, 34, 56, 500000000, time.UTC)
	if first.OpenedAt == nil || !first.OpenedAt.Equal(opened) || first.OpenedAt.Location() != time.UTC {
		t.Fatalf("opened clock not parsed to UTC: %v", first.OpenedAt)
	}
	nullable := byTitle["Bare registry row"][0]
	if nullable.OpenedAt != nil || nullable.Registry.OpenedRaw != "not a clock" || nullable.Registry.Size != nil || nullable.Registry.Pinned != nil {
		t.Fatal("invalid opened time or nullable size/pin fabricated")
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 2 || sources[0].Kind != RegistryDocumentKind || sources[0].Qualification != RegistryDocumentQualification ||
		sources[1].Coverage != "partial" || sources[1].Losses["office_registry_document_unmapped"] != 2 || sources[1].Losses["office_registry_ancestry_unmapped"] != 1 {
		t.Fatalf("registry sources or losses lost: %+v %v", sources, err)
	}
	for query, want := range map[string]int{"registry title": 2, "friendly": 2, "synthetic application": 1, "PowerPoint": 1, "Bare": 1, "12": 2} {
		if got := registryRows(t, s, DocumentFilter{Query: query}); len(got) != want {
			t.Fatalf("query %q found %d rows, want %d", query, len(got), want)
		}
	}
	detail, err := s.DocumentObservation(ctx, first.ID)
	if err != nil || detail.Source.Kind != RegistryDocumentKind || detail.Source.ID != first.SourceID {
		t.Fatalf("registry detail source: %v", err)
	}
	fields := map[string]int{}
	for _, reference := range detail.References {
		fields[reference.Field]++
		if reference.AssociationState != "unresolved" || reference.Application != "registry" || reference.Surface != "" {
			t.Fatalf("reference resolved or mislabeled: %+v", reference)
		}
	}
	if detail.ReferencesTotal != 5 || fields["url"] != 3 || fields["friendly_path"] != 2 {
		t.Fatalf("exact URL and friendly-path occurrences = %v (total %d)", fields, detail.ReferencesTotal)
	}
	var physical int
	if err := s.db.QueryRow("select physical_observations from document_capture_metadata where capture_id=?", b.ID).Scan(&physical); err != nil || physical != 3 {
		t.Fatalf("physical registry observations = %d, %v", physical, err)
	}
}

func TestRegistryRepresentationReuseAndFailure(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	locator := "/fixture/MicrosoftRegistrationDB_a.reg"
	first := beginRegistry(t, s, locator, base)
	if err := s.RecordKnowledgeFailure(ctx, first, KnowledgeOutcome{Status: "failed", Reason: "office_registry_source_busy", At: base}); err != nil {
		t.Fatal(err)
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].ID != 0 || sources[0].AdmittedID != 0 || sources[0].Reason != "office_registry_source_busy" ||
		sources[0].Locator != locator || sources[0].Kind != RegistryDocumentKind {
		t.Fatalf("first failure invented an admitted source: %+v %v", sources, err)
	}
	a, b := registryObservation(12, "State A"), registryObservation(12, "State B")
	at := func(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }
	one := beginRegistry(t, s, locator, at(1))
	admitRegistry(t, s, one, at(1), "complete", "digest-a", officeregistry.Result{Documents: []officeregistry.Observation{a}})
	two := beginRegistry(t, s, locator, at(2))
	admitRegistry(t, s, two, at(2), "complete", "digest-b", officeregistry.Result{Documents: []officeregistry.Observation{b}})
	three := beginRegistry(t, s, locator, at(3))
	admitRegistry(t, s, three, at(3), "partial", "digest-a2", officeregistry.Result{Documents: []officeregistry.Observation{a},
		Losses: []officeregistry.Loss{{Code: "office_registry_document_unmapped", Count: 1}}})
	rows := registryRows(t, s, DocumentFilter{})
	if len(rows) != 2 {
		t.Fatalf("A-B-A created %d representations, want 2", len(rows))
	}
	for _, row := range rows {
		switch row.Registry.Title {
		case "State A":
			if row.CaptureID != one.ID || row.Retained || !row.CapturedAt.Equal(at(1)) || !row.SourceObservedAt.Equal(at(1)) {
				t.Fatalf("reused representation lost its original clocks: %+v", row)
			}
		case "State B":
			if row.CaptureID != two.ID || !row.Retained {
				t.Fatalf("absent representation not retained: %+v", row)
			}
		}
	}
	four := beginRegistry(t, s, locator, at(4))
	admitRegistry(t, s, four, at(4), "unchanged", "digest-a2", officeregistry.Result{})
	sources, err = s.DocumentSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].AdmittedID != four.ID || sources[0].Coverage != "partial" ||
		sources[0].Losses["office_registry_document_unmapped"] != 1 || sources[0].Status != "unchanged" {
		t.Fatalf("unchanged capture lost partial losses: %+v %v", sources, err)
	}
	five := beginRegistry(t, s, locator, at(5))
	if err := s.RecordKnowledgeFailure(ctx, five, KnowledgeOutcome{Status: "failed", Reason: "office_registry_capture_failed", At: at(5)}); err != nil {
		t.Fatal(err)
	}
	sources, err = s.DocumentSources(ctx)
	if err != nil || sources[0].Status != "failed" || sources[0].AdmittedID != four.ID || sources[0].Reason != "office_registry_capture_failed" {
		t.Fatalf("failed-after-good did not keep the last good frontier: %+v %v", sources, err)
	}
	if rows := registryRows(t, s, DocumentFilter{Query: "State"}); len(rows) != 2 {
		t.Fatal("failed capture removed searchable rows")
	}
	// Reverse attempt frontier: an older attempt finishing after a newer one is never admitted.
	old := beginRegistry(t, s, locator, at(6))
	newer := beginRegistry(t, s, locator, at(7))
	admitRegistry(t, s, newer, at(7), "complete", "digest-b2", officeregistry.Result{Documents: []officeregistry.Observation{b}})
	err = s.CommitRegistryDocumentObservations(ctx, old, KnowledgeOutcome{Status: "complete", ContentDigest: "digest-old",
		Qualification: RegistryDocumentQualification, At: at(8)}, officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(99, "Stale")}},
		RegistryCaptureMetadata{})
	if !errors.Is(err, ErrStaleKnowledgeAttempt) || len(registryRows(t, s, DocumentFilter{Query: "Stale"})) != 0 {
		t.Fatalf("older attempt admitted past the frontier: %v", err)
	}
	if err := s.CommitRegistryDocumentObservations(ctx, beginRegistry(t, s, locator, at(9)), KnowledgeOutcome{Status: "unchanged",
		ContentDigest: "never-admitted", Qualification: RegistryDocumentQualification, At: at(9)}, officeregistry.Result{}, RegistryCaptureMetadata{}); err == nil {
		t.Fatal("unchanged outcome admitted for a digest that was never admitted")
	}
}

func TestRegistryDocumentSnapshotPublication(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	jsonAttempt := beginDocument(t, s, base)
	admitDocument(t, s, jsonAttempt, base, "complete", "json-one", []officedocuments.Document{documentFixture("Shared JSON document", 0)})
	before := dumpTable(t, s, "document_observations") + "\n" + dumpTable(t, s, "document_fts")
	registry := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_a.reg", base)
	admitRegistry(t, s, registry, base, "complete", "registry-one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Registry document")}})
	snapshot, err := s.DocumentSnapshot(ctx, DocumentFilter{})
	if err != nil || len(snapshot.Rows) != 2 || len(snapshot.Sources) != 2 {
		t.Fatalf("mixed snapshot: %+v %v", snapshot, err)
	}
	jsonRow, registryRow := snapshot.Rows[1], snapshot.Rows[0] // both modified times unknown: title order
	if jsonRow.Variant != DocumentVariantJSON || jsonRow.Registry != nil || jsonRow.OpenedAt != nil ||
		!reflect.DeepEqual(jsonRow.Document, documentFixture("Shared JSON document", 0)) || jsonRow.ID != 1 {
		t.Fatalf("existing JSON row changed: %+v", jsonRow)
	}
	// The JSON source's stored rows are byte-identical to what they were before any registry capture.
	after := dump(t, s)
	for _, line := range strings.Split(before, "\n") {
		if !strings.Contains(after, line) {
			t.Fatalf("stored JSON observation or FTS payload changed: %s", line)
		}
	}
	for _, filter := range []DocumentFilter{{Surface: officedocuments.Shared}, {Application: "supplied"}} {
		if rows := registryRows(t, s, filter); len(rows) != 1 || rows[0].Variant != DocumentVariantJSON {
			t.Fatal("JSON surface/application filters matched a registry row")
		}
	}
	if rows := registryRows(t, s, DocumentFilter{Application: "registry"}); len(rows) != 1 || rows[0].Variant != RegistryDocumentKind {
		t.Fatal("registry source label does not select registry rows")
	}
	selected, err := s.DocumentSnapshot(ctx, DocumentFilter{SourceID: registryRow.SourceID})
	if err != nil || len(selected.Rows) != 1 || len(selected.Sources) != 1 || selected.Sources[0].Kind != RegistryDocumentKind {
		t.Fatalf("registry source selection: %+v %v", selected, err)
	}
	if locators, err := s.DocumentLocators(ctx, 0); err != nil || len(locators) != 2 {
		t.Fatalf("registry locator missing from refresh set: %v %v", locators, err)
	}
	surface, application, err := s.DocumentLocatorInput(ctx, registry.LocatorID)
	if err != nil || surface != "" || application != "registry" {
		t.Fatalf("registry binding = %q %q %v", surface, application, err)
	}
}

func TestRegistryModifiedFiltersNeverUseOpened(t *testing.T) {
	s := newStore(t)
	registry := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_a.reg", base)
	admitRegistry(t, s, registry, base, "complete", "registry-one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Registry document")}})
	opened := time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC)
	for _, filter := range []DocumentFilter{
		{ModifiedSince: opened.Add(-time.Hour)}, {ModifiedUntil: opened.Add(time.Hour)},
		{ModifiedSince: base.Add(-time.Hour), ModifiedUntil: base.Add(time.Hour)},
	} {
		if rows := registryRows(t, s, filter); len(rows) != 0 {
			t.Fatalf("modified window matched a registry row through opened, capture or file time: %+v", filter)
		}
	}
	var modified any
	if err := s.db.QueryRow("select modified_at from document_observations").Scan(&modified); err != nil || modified != nil {
		t.Fatalf("registry modified_at = %v, want null", modified)
	}
	if rows := registryRows(t, s, DocumentFilter{}); len(rows) != 1 || rows[0].OpenedAt == nil {
		t.Fatal("unbounded read lost the registry row or its opened evidence")
	}
}

func TestRegistryZeroHitCoverageAfterGlobalLimit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	jsonAttempt := beginDocument(t, s, base)
	known := documentFixture("Zeta known modified", 0)
	known.Modified = officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: base}
	admitDocument(t, s, jsonAttempt, base, "complete", "json-one", []officedocuments.Document{known})
	good := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_a.reg", base)
	admitRegistry(t, s, good, base, "partial", "registry-one", officeregistry.Result{
		Documents: []officeregistry.Observation{registryObservation(12, "Alpha unknown modified")},
		Losses:    []officeregistry.Loss{{Code: "office_registry_document_unmapped", Count: 1}}})
	failed := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_b.reg", base)
	if err := s.RecordKnowledgeFailure(ctx, failed, KnowledgeOutcome{Status: "failed", Reason: "office_registry_source_busy", At: base}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.DocumentSnapshot(ctx, DocumentFilter{Limit: 1})
	if err != nil || len(snapshot.Rows) != 1 || !snapshot.Truncated || snapshot.Rows[0].Variant != DocumentVariantJSON {
		t.Fatalf("known modified time did not sort before unknown ahead of the limit: %+v %v", snapshot.Rows, err)
	}
	kinds := map[string]KnowledgeSource{}
	for _, source := range snapshot.Sources {
		kinds[source.Locator] = source
	}
	if len(snapshot.Sources) != 3 || kinds["/fixture/MicrosoftRegistrationDB_a.reg"].Coverage != "partial" ||
		kinds["/fixture/MicrosoftRegistrationDB_a.reg"].Losses["office_registry_document_unmapped"] != 1 ||
		kinds["/fixture/MicrosoftRegistrationDB_b.reg"].Reason != "office_registry_source_busy" || kinds["/fixture/MicrosoftRegistrationDB_b.reg"].ID != 0 {
		t.Fatalf("registry coverage did not survive zero clipped hits: %+v", snapshot.Sources)
	}
	bounded, err := s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: base.Add(-time.Hour), Limit: 5})
	if err != nil || len(bounded.Rows) != 1 || len(bounded.Sources) != 3 {
		t.Fatalf("unknown-modified registry row matched a window or coverage dropped: %+v %v", bounded, err)
	}
}

func TestMigrationSchema14AddsRegistryVariantWithoutTouchingJSONRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	attempt := beginDocument(t, s, base)
	admitDocument(t, s, attempt, base, "complete", "json-one", []officedocuments.Document{documentFixture("Kept JSON document", 0)})
	for _, statement := range []string{
		"alter table document_observations drop column variant", "alter table document_observations drop column registry_json",
		"alter table document_observations drop column opened_at", "alter table document_locator_metadata drop column variant",
		"alter table document_capture_metadata drop column variant", "update schema_migrations set version=13",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	state := func(s *Store) string {
		var out string
		if err := s.db.QueryRow(`select group_concat(rowid||'|'||source_id||'|'||capture_id||'|'||representation_digest||'|'||ordinal||'|'||surface||'|'||application||'|'||title||'|'||url||'|'||web_url||'|'||coalesce(modified_at,'')||'|'||document_json,'\n')
||(select group_concat(rowid||title||metadata,'\n') from document_fts) from document_observations`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := state(s)
	must0(s.Close())
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ro.DocumentSnapshot(ctx, DocumentFilter{Limit: 5})
	if err != nil || len(snapshot.Rows) != 1 || snapshot.Rows[0].Variant != DocumentVariantJSON || len(snapshot.Sources) != 1 {
		t.Fatalf("schema 13 read-only snapshot: %+v %v", snapshot, err)
	}
	if detail, err := ro.DocumentObservation(ctx, snapshot.Rows[0].ID); err != nil || detail.ReferencesTotal != 2 {
		t.Fatalf("schema 13 read-only detail: %v", err)
	}
	if surface, application, err := ro.DocumentLocatorInput(ctx, attempt.LocatorID); err != nil || surface != officedocuments.Shared || application != "supplied" {
		t.Fatalf("schema 13 read-only binding: %v", err)
	}
	if need, err := ro.NeedsUpgrade(ctx); err != nil || need {
		t.Fatalf("optional registry columns contaminated legacy freshness: %t %v", need, err)
	}
	if n := rowCount(t, ro, `select max(version) from schema_migrations`); n != 13 {
		t.Fatalf("read-only open migrated the archive to %d", n)
	}
	must0(ro.Close())
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := rowCount(t, s, `select max(version) from schema_migrations`); n != SchemaVersion || SchemaVersion < 14 {
		t.Fatalf("schema %d, want the current version, 14 or later", n)
	}
	if after := state(s); after != before {
		t.Fatal("migration changed existing JSON rows or their FTS payloads")
	}
	if n := rowCount(t, s, `select count(*) from document_observations where variant='office_json' and registry_json is null and opened_at is null`); n != 1 {
		t.Fatal("existing JSON row did not receive the JSON discriminator")
	}
	registry := beginRegistry(t, s, "/fixture/MicrosoftRegistrationDB_a.reg", base)
	admitRegistry(t, s, registry, base, "complete", "registry-one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Registry document")}})
	if rows := registryRows(t, s, DocumentFilter{}); len(rows) != 2 {
		t.Fatal("migrated archive did not accept the registry variant")
	}
}
