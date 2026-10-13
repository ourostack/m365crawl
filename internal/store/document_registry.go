package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/officeregistry"
)

const (
	// RegistryDocumentKind is both the knowledge kind of an Office registry source and the variant
	// of its locator, observation and capture rows.
	RegistryDocumentKind = "office_registry"
	// RegistryDocumentQualification names the reader and mapper a registry representation came from.
	RegistryDocumentQualification = "office-registry-v1/mapper-v1"
	// RegistryDocumentSource is the fixed source label a registry row carries where an Office JSON
	// row carries its application. The registry's own application claims stay in the payload.
	RegistryDocumentSource = "registry"
)

// RegistryCaptureMetadata are the immutable clocks of one registry acquisition.
type RegistryCaptureMetadata struct{ AcquiredAt, SourceObservedAt *time.Time }

// registryOpened is the published grammar for a registry opened time: RFC 3339 with an explicit
// Z or numeric offset and at most nine fractional digits. It is deliberately conservative: a
// value outside it keeps its raw text and has no parsed time.
var registryOpened = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)

// ParseRegistryOpened parses a registry opened time to UTC, or returns nil. It never falls back
// to the host locale, a file time or a capture time.
func ParseRegistryOpened(raw string) *time.Time {
	if !registryOpened.MatchString(raw) {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	at = at.UTC()
	return &at
}

func (s *Store) BeginRegistryDocumentAttempt(ctx context.Context, input KnowledgeInput) (KnowledgeAttempt, error) {
	if input.Kind != RegistryDocumentKind {
		return KnowledgeAttempt{}, errors.New("documents: invalid registry source binding")
	}
	return s.beginDocumentAttempt(ctx, input, documentBinding{Variant: RegistryDocumentKind, Application: RegistryDocumentSource})
}

// CommitRegistryDocumentObservations admits one registry capture through the same immutable
// representation, membership and frontier machinery as Office JSON. Each full native observation
// plus its source binding is one representation; a repeated one keeps its original clocks.
func (s *Store) CommitRegistryDocumentObservations(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result officeregistry.Result, capture RegistryCaptureMetadata) error {
	if !slices.Contains([]string{"complete", "partial", "unchanged"}, outcome.Status) ||
		outcome.ContentDigest == "" || outcome.Qualification != RegistryDocumentQualification ||
		(outcome.Status == "unchanged" && (len(result.Documents) != 0 || len(result.Losses) != 0)) ||
		(outcome.Status != "unchanged" && (len(result.Losses) > 0) != (outcome.Status == "partial")) {
		return errors.New("documents: unqualified registry outcome")
	}
	for _, document := range result.Documents {
		if strings.TrimSpace(document.Title) == "" || strings.TrimSpace(document.URL) == "" {
			return errors.New("documents: invalid native registry representation")
		}
	}
	admission := documentAdmission{
		binding:       documentBinding{Variant: RegistryDocumentKind, Application: RegistryDocumentSource},
		qualification: RegistryDocumentQualification, unmappedLoss: "office_registry_document_unmapped",
		documents: len(result.Documents), acquired: capture.AcquiredAt, observed: capture.SourceObservedAt,
		apply: func(ctx context.Context, tx *sql.Tx, sourceID, captureID int64) error {
			return applyRegistryObservations(ctx, tx, attempt.LocatorID, sourceID, captureID, result)
		},
	}
	for _, loss := range result.Losses {
		if loss.Code == "" || loss.Count <= 0 {
			return errors.New("documents: invalid registry source loss")
		}
		admission.losses = append(admission.losses, documentLoss{loss.Code, loss.Count})
	}
	return s.finishKnowledge(ctx, attempt, outcome, RegistryDocumentKind, admission.admit)
}

func applyRegistryObservations(ctx context.Context, tx *sql.Tx, locatorID, sourceID, captureID int64, result officeregistry.Result) error {
	var platform, locator string
	if err := tx.QueryRowContext(ctx, `select platform,locator from knowledge_locators where id=?`, locatorID).Scan(&platform, &locator); err != nil {
		return err
	}
	for ordinal, document := range result.Documents {
		encoded, _ := json.Marshal(document) // strings, an integer and nullable scalars always encode
		var opened any
		if at := ParseRegistryOpened(document.OpenedRaw); at != nil {
			opened = knowledgeTime(*at)
		}
		err := publishDocumentObservation(ctx, tx, documentPublication{sourceID: sourceID, captureID: captureID,
			digest:  hashOf(strings.Join([]string{RegistryDocumentKind, RegistryDocumentQualification, platform, locator, string(encoded)}, "\x00")),
			ordinal: ordinal, variant: RegistryDocumentKind, application: RegistryDocumentSource,
			title: document.Title, url: document.URL, registryJSON: string(encoded), opened: opened,
			metadata: registryMetadataText(document)})
		if err != nil {
			return err
		}
	}
	return nil
}

// registryMetadataText is what the document index holds beside the title: only the retained
// native claims, never document text or any other string of the registry file.
func registryMetadataText(document officeregistry.Observation) string {
	return strings.Join([]string{document.URL, document.FriendlyPath, document.ApplicationPath,
		document.ApplicationValue, strconv.FormatInt(document.NodeID, 10)}, "\n")
}

// decodeRegistryRow validates a stored registry row's discriminator, label and payload together
// and attaches the native observation and its parsed opened time.
func decodeRegistryRow(row *DocumentObservationRow, stored documentStored) error {
	if !stored.registry.Valid || stored.document != "" || stored.surface != "" || row.Application != RegistryDocumentSource ||
		stored.kind != RegistryDocumentKind {
		return errors.New("documents: invalid stored registry representation")
	}
	decoder := json.NewDecoder(strings.NewReader(stored.registry.String))
	decoder.DisallowUnknownFields()
	var observation officeregistry.Observation
	if err := decoder.Decode(&observation); err != nil {
		return errors.New("documents: invalid stored registry payload")
	}
	times, err := knowledgeTimes(stored.opened)
	if err != nil {
		return err
	}
	want := ParseRegistryOpened(observation.OpenedRaw)
	if (want == nil) != (times[0] == nil) || (want != nil && !want.Equal(*times[0])) {
		return errors.New("documents: stored registry opened time does not match its payload")
	}
	row.Registry, row.OpenedAt = &observation, want
	return nil
}
