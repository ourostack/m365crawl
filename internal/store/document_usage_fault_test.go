package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

func usageFixture() onedriveusage.Result {
	return onedriveusage.Result{
		Recent: []onedriveusage.RecentObservation{usageRecent("native-1"), usageRecent("native-2")},
		Documents: []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Original", usageShare("Alpha", "One")),
			usageDocument("native-3", "variant-a", "Orphan of no recent row")},
		Collaborators: []onedriveusage.Collaborator{usageCollaborator("person-1")}}
}

func setupUsageObservation(t *testing.T, s *Store) KnowledgeAttempt {
	t.Helper()
	attempt := beginUsage(t, s, usageLocator, base)
	admitUsage(t, s, attempt, base, "complete", "one", usageFixture())
	return attempt
}

func TestUsageDocumentAtomicRegistrationAndAdmissionFaults(t *testing.T) {
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.BeginUsageDocumentAttempt(ctx, KnowledgeInput{Kind: UsageDocumentKind, Platform: "synthetic", Locator: usageLocator, At: base})
		return err
	})
	for _, status := range []string{"complete", "partial", "unchanged"} {
		t.Run(status, func(t *testing.T) {
			var attempt KnowledgeAttempt
			setup := func(t *testing.T, s *Store) {
				setupUsageObservation(t, s)
				attempt = beginUsage(t, s, usageLocator, base.Add(time.Hour))
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				result := usageFixture()
				result.Documents[0].Title = "Changed"
				outcome := KnowledgeOutcome{Status: status, ContentDigest: "two", Qualification: UsageDocumentQualification, At: base}
				if status == "partial" {
					result.Losses = []onedriveusage.Loss{{Code: "onedrive_usage_document_unmapped", Count: 1}}
				}
				if status == "unchanged" {
					result = onedriveusage.Result{}
					outcome.ContentDigest = "one"
				}
				return s.CommitUsageDocumentObservations(ctx, attempt, outcome, result, UsageCaptureMetadata{})
			})
		})
	}
}

func TestUsageDocumentReadFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) { setupUsageObservation(t, s) }
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentObservations(ctx, DocumentFilter{Query: "native-1", SourceID: 1, Limit: 1})
		return err
	})
	// Rows 1 and 2 are the recent rows, 3 the related variant, 5 the collaborator.
	for _, id := range []int64{1, 3, 5} {
		sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.DocumentObservation(ctx, id)
			return err
		})
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.DocumentSnapshot(ctx, DocumentFilter{Limit: 1})
		return err
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentLocatorInput(ctx, 1)
		return err
	})
}

func TestUsageDocumentInvalidInputsAndCorruptDiscriminators(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := setupUsageObservation(t, s)
	if _, err := s.BeginUsageDocumentAttempt(ctx, KnowledgeInput{Kind: RegistryDocumentKind, Platform: "synthetic", Locator: usageLocator, At: base}); err == nil {
		t.Fatal("usage attempt began under a registry kind")
	}
	if _, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "office_recent", Platform: "synthetic", Locator: "/fixture/other.json", At: base},
		officedocuments.Recent, UsageDocumentSource); err == nil {
		t.Fatal("usage source label accepted as a JSON application")
	}
	valid := usageFixture()
	good := KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: UsageDocumentQualification, At: base}
	withOutcome := func(change func(*KnowledgeOutcome)) KnowledgeOutcome {
		out := good
		change(&out)
		return out
	}
	recent, document, collaborator := usageRecent("native-1"), usageDocument("native-1", "variant-a", "Original"), usageCollaborator("person-1")
	blankRecent, blankDocument, blankFormat, blankCollaborator := recent, document, document, collaborator
	blankRecent.ID, blankDocument.ID, blankFormat.Format, blankCollaborator.ID = " ", "", "", ""
	for name, tc := range map[string]struct {
		outcome KnowledgeOutcome
		result  onedriveusage.Result
	}{
		"failed status":          {withOutcome(func(o *KnowledgeOutcome) { o.Status = "failed" }), valid},
		"missing digest":         {withOutcome(func(o *KnowledgeOutcome) { o.ContentDigest = "" }), valid},
		"registry qualification": {withOutcome(func(o *KnowledgeOutcome) { o.Qualification = RegistryDocumentQualification }), valid},
		"partial sans losses":    {withOutcome(func(o *KnowledgeOutcome) { o.Status = "partial" }), valid},
		"complete with losses":   {good, onedriveusage.Result{Losses: []onedriveusage.Loss{{Code: "onedrive_usage_recent_unmapped", Count: 1}}}},
		"unchanged with rows":    {withOutcome(func(o *KnowledgeOutcome) { o.Status = "unchanged" }), valid},
		"unchanged with losses": {withOutcome(func(o *KnowledgeOutcome) { o.Status = "unchanged" }),
			onedriveusage.Result{Losses: []onedriveusage.Loss{{Code: "onedrive_usage_recent_unmapped", Count: 1}}}},
		"blank recent ID":       {good, onedriveusage.Result{Recent: []onedriveusage.RecentObservation{blankRecent}}},
		"blank document ID":     {good, onedriveusage.Result{Documents: []onedriveusage.DocumentObservation{blankDocument}}},
		"blank document format": {good, onedriveusage.Result{Documents: []onedriveusage.DocumentObservation{blankFormat}}},
		"blank collaborator ID": {good, onedriveusage.Result{Collaborators: []onedriveusage.Collaborator{blankCollaborator}}},
		"repeated recent ID":    {good, onedriveusage.Result{Recent: []onedriveusage.RecentObservation{recent, recent}}},
		"repeated variant":      {good, onedriveusage.Result{Documents: []onedriveusage.DocumentObservation{document, document}}},
		"repeated collaborator": {good, onedriveusage.Result{Collaborators: []onedriveusage.Collaborator{collaborator, collaborator}}},
		"loss without a code":   {withOutcome(func(o *KnowledgeOutcome) { o.Status = "partial" }), onedriveusage.Result{Losses: []onedriveusage.Loss{{Count: 1}}}},
		"loss without a count":  {withOutcome(func(o *KnowledgeOutcome) { o.Status = "partial" }), onedriveusage.Result{Losses: []onedriveusage.Loss{{Code: "onedrive_usage_recent_unmapped"}}}},
	} {
		if err := s.CommitUsageDocumentObservations(ctx, a, tc.outcome, tc.result, UsageCaptureMetadata{}); err == nil {
			t.Fatalf("%s admitted", name)
		}
	}
	registry := beginRegistry(t, s, registryLocator, base)
	if err := s.CommitUsageDocumentObservations(ctx, registry, good, valid, UsageCaptureMetadata{}); err == nil {
		t.Fatal("usage result admitted into a registry locator")
	}
	for _, native := range []UsageNative{{}, {Type: "other", Recent: &recent}, {Type: UsageTypeRecent, Document: &document},
		{Type: UsageTypeDocument, Collaborator: &collaborator}, {Type: UsageTypeCollaborator, Recent: &recent},
		{Type: UsageTypeRecent, Recent: &recent, Document: &document}} {
		if id, format, ok := native.identity(); ok || id != "" || format != "" || usageNativeID(native) != "" {
			t.Fatalf("native record with a mismatched or repeated payload accepted: %+v", native)
		}
	}
	if UsageModifiedRaw(UsageNative{Type: UsageTypeCollaborator, Collaborator: &collaborator}) != "" ||
		ParseUsageClock("2026-10-09T12:34:56") != nil || ParseUsageClock("") != nil {
		t.Fatal("a collaborator or an offset-less clock produced a modification time")
	}
	// Row 1 is a recent row, row 3 the related document variant, row 5 the collaborator.
	for _, mutation := range []string{
		"update document_observations set variant='other' where rowid=3",
		"update document_observations set variant='office_json' where rowid=3",
		"update document_observations set variant='office_registry' where rowid=3",
		"update document_observations set document_json='{}' where rowid=3",
		"update document_observations set surface='recent' where rowid=3",
		"update document_observations set application='registry' where rowid=3",
		"update document_observations set registry_json='{}' where rowid=3",
		"update document_observations set opened_at='2026-10-09T12:34:56.000000000Z' where rowid=3",
		"update document_observations set usage_json=null where rowid=3",
		"update document_observations set usage_json='bad' where rowid=3",
		// The complete valid payload plus one unknown field: only strict decoding refuses it.
		"update document_observations set usage_json=json_set(usage_json,'$.Invented',1) where rowid=3",
		"update document_observations set usage_json=json_set(usage_json,'$.Document.Invented',1) where rowid=3",
		"update document_observations set usage_json=json_set(usage_json,'$.Type','recent') where rowid=3",
		"update document_observations set usage_json=json_set(usage_json,'$.Document.Format','variant-z') where rowid=3",
		"update document_observations set usage_json=json_set(usage_json,'$.Document.ModifiedRaw','2026-10-09T14:34:57Z') where rowid=3",
		"update document_observations set usage_json=json_set(usage_json,'$.Document.ModifiedRaw','') where rowid=3",
		"update document_observations set native_key='' where rowid=3",
		"update document_observations set modified_at=null where rowid=3",
		"update document_observations set modified_at='bad' where rowid=3",
		"update document_observations set modified_at='2026-10-09T12:34:57.000000000Z' where rowid=3",
		"update knowledge_locators set kind='office_registry'",
		"update document_capture_metadata set acquired_at='bad'",
		// Relations: an edge to a missing row, to a row of the wrong type, and to another native ID.
		"update document_usage_relations set recent_rowid=999",
		"update document_usage_relations set recent_rowid=5",
		"update document_usage_relations set recent_rowid=2",
	} {
		t.Run(mutation, func(t *testing.T) {
			local := newStore(t)
			setupUsageObservation(t, local)
			if _, err := local.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := local.DocumentObservation(ctx, 3); err == nil {
				t.Fatal("corrupt usage row or relation silently returned")
			}
		})
	}
	// An Office JSON or registry row that carries a usage payload is refused too.
	for _, setup := range []func(*testing.T, *Store){func(t *testing.T, s *Store) { setupDocumentObservation(t, s) },
		func(t *testing.T, s *Store) { setupRegistryObservation(t, s) }} {
		local := newStore(t)
		setup(t, local)
		if _, err := local.db.Exec("update document_observations set usage_json='{}'"); err != nil {
			t.Fatal(err)
		}
		if _, err := local.DocumentObservation(ctx, 1); err == nil {
			t.Fatal("a non-usage row with a usage payload was accepted")
		}
	}
}
