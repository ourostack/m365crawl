package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
)

const registryLocator = "/fixture/MicrosoftRegistrationDB_a.reg"

func setupRegistryObservation(t *testing.T, s *Store) KnowledgeAttempt {
	t.Helper()
	attempt := beginRegistry(t, s, registryLocator, base)
	admitRegistry(t, s, attempt, base, "complete", "one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Original")}})
	return attempt
}

func TestRegistryDocumentAtomicRegistrationAndAdmissionFaults(t *testing.T) {
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.BeginRegistryDocumentAttempt(ctx, KnowledgeInput{Kind: RegistryDocumentKind, Platform: "synthetic",
			Locator: registryLocator, At: base})
		return err
	})
	for _, status := range []string{"complete", "partial", "unchanged"} {
		t.Run(status, func(t *testing.T) {
			var attempt KnowledgeAttempt
			setup := func(t *testing.T, s *Store) {
				setupRegistryObservation(t, s)
				attempt = beginRegistry(t, s, registryLocator, base.Add(time.Hour))
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				result := officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(13, "Changed")}}
				outcome := KnowledgeOutcome{Status: status, ContentDigest: "two", Qualification: RegistryDocumentQualification, At: base}
				if status == "partial" {
					result.Losses = []officeregistry.Loss{{Code: "office_registry_document_unmapped", Count: 1}}
				}
				if status == "unchanged" {
					result = officeregistry.Result{}
					outcome.ContentDigest = "one"
				}
				return s.CommitRegistryDocumentObservations(ctx, attempt, outcome, result, RegistryCaptureMetadata{})
			})
		})
	}
}

func TestRegistryDocumentReadFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) { setupRegistryObservation(t, s) }
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentObservations(ctx, DocumentFilter{Query: "Original", SourceID: 1, Limit: 1})
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.DocumentObservation(ctx, 1)
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.DocumentSnapshot(ctx, DocumentFilter{Limit: 1})
		return err
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentLocatorInput(ctx, 1)
		return err
	})
}

func TestRegistryDocumentInvalidInputsAndCorruptDiscriminators(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := setupRegistryObservation(t, s)
	if _, err := s.BeginRegistryDocumentAttempt(ctx, KnowledgeInput{Kind: "office_recent", Platform: "synthetic", Locator: registryLocator, At: base}); err == nil {
		t.Fatal("registry attempt began under a JSON kind")
	}
	if _, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "office_recent", Platform: "synthetic", Locator: registryLocator, At: base},
		officedocuments.Recent, "registry"); err == nil {
		t.Fatal("registry source label accepted as a JSON application")
	}
	valid := officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Original")}}
	for name, tc := range map[string]struct {
		outcome KnowledgeOutcome
		result  officeregistry.Result
	}{
		"failed status":       {KnowledgeOutcome{Status: "failed", At: base}, valid},
		"missing digest":      {KnowledgeOutcome{Status: "complete", Qualification: RegistryDocumentQualification, At: base}, valid},
		"json qualification":  {KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: DocumentQualification, At: base}, valid},
		"partial sans losses": {KnowledgeOutcome{Status: "partial", ContentDigest: "one", Qualification: RegistryDocumentQualification, At: base}, valid},
		"unchanged with rows": {KnowledgeOutcome{Status: "unchanged", ContentDigest: "one", Qualification: RegistryDocumentQualification, At: base}, valid},
		"blank title": {KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: RegistryDocumentQualification, At: base},
			officeregistry.Result{Documents: []officeregistry.Observation{{NodeID: 1, URL: "file:///synthetic"}}}},
		"invalid loss": {KnowledgeOutcome{Status: "partial", ContentDigest: "one", Qualification: RegistryDocumentQualification, At: base},
			officeregistry.Result{Losses: []officeregistry.Loss{{Code: "", Count: 1}}}},
	} {
		if err := s.CommitRegistryDocumentObservations(ctx, a, tc.outcome, tc.result, RegistryCaptureMetadata{}); err == nil {
			t.Fatalf("%s admitted", name)
		}
	}
	jsonAttempt := beginDocument(t, s, base)
	if err := s.CommitRegistryDocumentObservations(ctx, jsonAttempt, KnowledgeOutcome{Status: "complete", ContentDigest: "one",
		Qualification: RegistryDocumentQualification, At: base}, valid, RegistryCaptureMetadata{}); err == nil {
		t.Fatal("registry result admitted into a JSON locator")
	}
	if ParseRegistryOpened("2026-10-09T12:34:56") != nil || ParseRegistryOpened("2026-10-09 12:34:56Z") != nil ||
		ParseRegistryOpened("2026-13-40T12:34:56Z") != nil || ParseRegistryOpened("") != nil {
		t.Fatal("opened time outside the published grammar was parsed")
	}
	for _, mutation := range []string{
		"update document_observations set variant='other'",
		"update document_observations set variant='office_json'",
		"update document_observations set document_json='{}'",
		"update document_observations set surface='recent'",
		"update document_observations set application='Word'",
		"update document_observations set registry_json=null",
		"update document_observations set registry_json='bad'",
		`update document_observations set registry_json='{"NodeID":1,"Invented":true}'`,
		"update document_observations set opened_at=null",
		"update document_observations set opened_at='bad'",
		"update document_observations set opened_at='2026-10-09T12:34:57.000000000Z'",
		"update knowledge_locators set kind='office_recent'",
		"update document_capture_metadata set acquired_at='bad'",
	} {
		t.Run(mutation, func(t *testing.T) {
			local := newStore(t)
			setupRegistryObservation(t, local)
			if _, err := local.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := local.DocumentObservation(ctx, 1); err == nil {
				t.Fatal("corrupt registry discriminator silently returned")
			}
			if rows, _, err := local.DocumentObservations(ctx, DocumentFilter{}); err == nil || len(rows) != 0 {
				t.Fatal("corrupt registry discriminator became rows or empty success")
			}
		})
	}
	for _, mutation := range []string{
		"update document_locator_metadata set variant='other'",
		"update document_locator_metadata set surface='recent'",
		"update document_locator_metadata set application='Word'",
	} {
		local := newStore(t)
		attempt := setupRegistryObservation(t, local)
		if _, err := local.db.Exec(mutation); err != nil {
			t.Fatal(err)
		}
		if _, _, err := local.DocumentLocatorInput(ctx, attempt.LocatorID); err == nil {
			t.Fatalf("corrupt locator binding accepted: %s", mutation)
		}
	}
	jsonLocal := newStore(t)
	setupDocumentObservation(t, jsonLocal)
	for _, mutation := range []string{"update document_observations set registry_json='{}'", "update document_observations set opened_at='2026-10-09T12:34:56.000000000Z'"} {
		if _, err := jsonLocal.db.Exec(mutation); err != nil {
			t.Fatal(err)
		}
		if _, err := jsonLocal.DocumentObservation(ctx, 1); err == nil {
			t.Fatalf("JSON row with a registry payload accepted: %s", mutation)
		}
		if _, err := jsonLocal.db.Exec("update document_observations set registry_json=null,opened_at=null"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationSchema14RepairsPartialColumnsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	setupDocumentObservation(t, s)
	before := dump(t, s)
	for _, statement := range []string{"alter table document_observations drop column registry_json",
		"alter table document_locator_metadata drop column variant"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := dump(t, s); after != before {
		t.Fatalf("partial repair changed stored rows\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The capture table is gone, so its statement fails after the observation statements ran: all roll back.
	for _, statement := range []string{"alter table document_observations drop column variant",
		"alter table document_observations drop column opened_at", "drop table document_capture_metadata"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	partial := dump2(t, s)
	if err := s.migrate(ctx); err == nil {
		t.Fatal("migration succeeded although one statement cannot run")
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('document_observations') where name in ('variant','opened_at')`); n != 0 {
		t.Fatalf("a failed migration left %d of its columns applied", n)
	}
	if after := dump2(t, s); after != partial {
		t.Fatal("failed migration changed stored rows")
	}
}

// dump2 reads the observation rows directly: dump needs every table, and one was dropped on purpose.
func dump2(t *testing.T, s *Store) string {
	t.Helper()
	var out string
	if err := s.db.QueryRow(`select group_concat(rowid||'|'||title||'|'||document_json||'|'||representation_digest,'\n') from document_observations`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
