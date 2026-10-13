package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
)

const (
	DocumentQualification = "office-json-v1/mapper-v1"
	// DocumentVariantJSON is the variant of every Office JSON locator, observation and capture.
	DocumentVariantJSON = "office_json"
)

var ErrDocumentObservationNotFound = errors.New("documents: observation not found")

type DocumentCaptureMetadata struct {
	Surface                      officedocuments.Surface
	Application                  string
	AcquiredAt, SourceObservedAt *time.Time
}

// DocumentObservationRow is one immutable native representation. Variant says which native
// payload it carries: an Office JSON row has Document and no Registry; an Office registry row has
// Registry, a zero Document, the fixed Application "registry" and OpenedAt, the registry's own
// opened time parsed to UTC or nil. A registry row never has a native modified time. A OneDrive
// usage row has Usage, a zero Document, the fixed Application "onedrive" and ModifiedAt, the
// record's own modification time parsed to UTC or nil; an Office JSON row keeps its modification
// time in Document.Modified.
type DocumentObservationRow struct {
	ID, SourceID, CaptureID      int64
	Variant                      string
	Document                     officedocuments.Document
	Registry                     *officeregistry.Observation
	OpenedAt                     *time.Time
	Usage                        *UsageNative
	ModifiedAt                   *time.Time
	Application                  string
	CapturedAt, SourceObservedAt *time.Time
	Retained                     bool
}

type DocumentObservationSnapshot struct {
	Rows      []DocumentObservationRow
	Sources   []KnowledgeSource
	Truncated bool
	// Undated counts the rows a date window excluded because they have no modification time,
	// among the rows the other filters select: by variant, and for a OneDrive usage row by
	// variant, an underscore and its native type. Nil when no window was asked for.
	Undated map[string]int
}

// The orders of a document list. Each is total: ties fall through to the default order's keys.
const (
	DocumentOrderModified = "modified_at"
	DocumentOrderOpened   = "opened_at"
)

type DocumentFilter struct {
	Query                        string
	SourceID                     int64
	Application                  string
	Surface                      officedocuments.Surface
	ModifiedSince, ModifiedUntil time.Time
	Limit                        int
	// OrderBy is DocumentOrderModified (the default, also when empty) or DocumentOrderOpened.
	OrderBy string
}

type DocumentReferenceObservation struct {
	ObservationID, SourceID, CaptureID int64
	Field, URL, Application            string
	Surface                            officedocuments.Surface
	AssociationState                   string
}

type DocumentObservationDetail struct {
	Observation         DocumentObservationRow
	Source              KnowledgeSource
	References          []DocumentReferenceObservation
	ReferencesTotal     int
	ReferencesTruncated bool
	// UsageRelations are a usage row's capture-owned edges, the first 100 in native-key order,
	// read from UsageRelationsCaptureID in the same transaction as the row. The total is exact.
	UsageRelations          []DocumentUsageRelation
	UsageRelationsTotal     int
	UsageRelationsTruncated bool
	UsageRelationsCaptureID int64
}

func DocumentKind(surface officedocuments.Surface) string {
	switch surface {
	case officedocuments.Recent:
		return "office_recent"
	case officedocuments.Shared:
		return "office_shared"
	case officedocuments.Recommended:
		return "office_recommended"
	case officedocuments.Dialog:
		return "office_dialog"
	default:
		return ""
	}
}

func validDocumentBinding(surface officedocuments.Surface, application string) bool {
	return DocumentKind(surface) != "" && slices.Contains([]string{"Word", "Excel", "PowerPoint", "supplied"}, application)
}

// documentBinding is a locator's immutable native binding.
type documentBinding struct {
	Variant     string
	Surface     officedocuments.Surface
	Application string
}

// kind is the knowledge kind a binding belongs to, or "" when the combination is invalid.
func (b documentBinding) kind() string {
	switch {
	case b.Variant == DocumentVariantJSON && validDocumentBinding(b.Surface, b.Application):
		return DocumentKind(b.Surface)
	case b.Variant == RegistryDocumentKind && b.Surface == "" && b.Application == RegistryDocumentSource:
		return RegistryDocumentKind
	case b == usageBinding:
		return UsageDocumentKind
	}
	return ""
}

func (s *Store) BeginDocumentAttempt(ctx context.Context, input KnowledgeInput, surface officedocuments.Surface, application string) (KnowledgeAttempt, error) {
	if !validDocumentBinding(surface, application) || input.Kind != DocumentKind(surface) {
		return KnowledgeAttempt{}, errors.New("documents: invalid source binding")
	}
	return s.beginDocumentAttempt(ctx, input, documentBinding{Variant: DocumentVariantJSON, Surface: surface, Application: application})
}

func (s *Store) beginDocumentAttempt(ctx context.Context, input KnowledgeInput, binding documentBinding) (KnowledgeAttempt, error) {
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, locatorID int64) error {
		if _, err := tx.ExecContext(ctx, `insert into document_locator_metadata(locator_id,surface,application,variant)
values(?,?,?,?) on conflict(locator_id) do nothing`, locatorID, binding.Surface, binding.Application, binding.Variant); err != nil {
			return err
		}
		registered, err := readDocumentLocator(ctx, tx, locatorID, true)
		if err != nil {
			return err
		}
		if registered != binding {
			return errors.New("documents: source binding changed")
		}
		return nil
	}, nil)
}

// readDocumentLocator reads a locator's binding and refuses any variant, surface, application and
// kind combination that no capture could have registered. variants is false on an archive from
// before schema 14, where every locator is Office JSON.
func readDocumentLocator(ctx context.Context, reader knowledgeReader, id int64, variants bool) (documentBinding, error) {
	var binding documentBinding
	var kind string
	variant := "'" + DocumentVariantJSON + "'"
	if variants {
		variant = "d.variant"
	}
	err := reader.QueryRowContext(ctx, `select d.surface,d.application,l.kind,`+variant+`
from document_locator_metadata d join knowledge_locators l on l.id=d.locator_id where d.locator_id=?`, id).
		Scan(&binding.Surface, &binding.Application, &kind, &binding.Variant)
	if err != nil {
		return documentBinding{}, err
	}
	if kind == "" || binding.kind() != kind {
		return documentBinding{}, errors.New("documents: invalid stored source binding")
	}
	return binding, nil
}

// DocumentLocatorInput returns a registered locator's surface and application. An Office registry
// locator has an empty surface and the fixed source label "registry"; a OneDrive usage locator has
// an empty surface and the fixed source label "onedrive".
func (s *Store) DocumentLocatorInput(ctx context.Context, id int64) (officedocuments.Surface, string, error) {
	available, variants, _, err := documentSchema(ctx, s.db)
	if err != nil {
		return "", "", err
	}
	if !available || id <= 0 {
		return "", "", ErrKnowledgeSourceUnknown
	}
	binding, err := readDocumentLocator(ctx, s.db, id, variants)
	return binding.Surface, binding.Application, err
}

// documentSchema reports whether the document tables exist, whether they carry the schema 14
// variant columns, and whether they carry the schema 15 usage payload, native key and relation
// table. The optional membership pointer is detected independently. A read-only archive from
// before schema 14 has the tables without the variant columns: its rows are all Office JSON.
// One from before schema 15 has no usage rows to read. Neither is migrated.
func documentSchema(ctx context.Context, reader knowledgeReader) (tables, variants, usage bool, err error) {
	var tableCount, columnCount, usageCount int
	err = reader.QueryRowContext(ctx, `select
(select count(*) from sqlite_master where type='table'
 and name in ('document_observations','document_capture_members','document_capture_metadata','document_fts','document_locator_metadata')),
(select count(*) from pragma_table_info('document_observations') where name in ('variant','registry_json','opened_at'))+
(select count(*) from pragma_table_info('document_locator_metadata') where name='variant')+
(select count(*) from pragma_table_info('document_capture_metadata') where name='variant'),
(select count(*) from pragma_table_info('document_observations') where name in ('usage_json','native_key'))+
(select count(*) from sqlite_master where type='table' and name='document_usage_relations')`).Scan(&tableCount, &columnCount, &usageCount)
	return tableCount == 5, columnCount == 5, columnCount == 5 && usageCount == 3, err
}

// documentMembersOwner handles both shared membership and older physical schemas whose
// unchanged captures copied their own members and relations.
func documentMembersOwner(ctx context.Context, reader knowledgeReader, alias string) (string, error) {
	var pointers int
	err := reader.QueryRowContext(ctx, `select count(*) from pragma_table_info('document_capture_metadata') where name='members_capture'`).Scan(&pointers)
	if pointers == 0 {
		return alias + ".capture_id", err
	}
	return "coalesce(" + alias + ".members_capture," + alias + ".capture_id)", err
}

func (s *Store) CommitDocumentObservations(ctx context.Context, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result officedocuments.Result, capture DocumentCaptureMetadata) error {
	if !slices.Contains([]string{"complete", "partial", "unchanged"}, outcome.Status) ||
		outcome.ContentDigest == "" || outcome.Qualification != DocumentQualification ||
		!validDocumentBinding(capture.Surface, capture.Application) ||
		(outcome.Status == "unchanged" && (len(result.Documents) != 0 || len(result.Losses) != 0)) ||
		(outcome.Status != "unchanged" && (len(result.Losses) > 0) != (outcome.Status == "partial")) {
		return errors.New("documents: unqualified outcome")
	}
	for _, document := range result.Documents {
		if document.Surface != capture.Surface || strings.TrimSpace(document.Title) == "" || strings.TrimSpace(document.URL) == "" {
			return errors.New("documents: invalid native representation")
		}
	}
	for _, loss := range result.Losses {
		if loss.Code == "" || loss.Count <= 0 {
			return errors.New("documents: invalid source loss")
		}
	}
	admission := documentAdmission{
		binding:       documentBinding{Variant: DocumentVariantJSON, Surface: capture.Surface, Application: capture.Application},
		qualification: DocumentQualification, unmappedLosses: []string{"office_document_unmapped"}, documents: len(result.Documents),
		acquired: capture.AcquiredAt, observed: capture.SourceObservedAt,
		apply: func(ctx context.Context, tx *sql.Tx, sourceID, captureID int64) error {
			return applyDocumentObservations(ctx, tx, sourceID, captureID, result, capture.Application)
		},
	}
	for _, loss := range result.Losses {
		admission.losses = append(admission.losses, documentLoss{loss.Code, loss.Count})
	}
	return s.finishKnowledge(ctx, attempt, outcome, DocumentKind(capture.Surface), admission.admit)
}

type documentLoss struct {
	code  string
	count int
}

// documentAdmission is one variant's capture, admitted through the shared source frontier.
// unmappedLosses are the loss codes that each count one physical source row.
type documentAdmission struct {
	binding            documentBinding
	qualification      string
	unmappedLosses     []string
	documents          int
	losses             []documentLoss
	acquired, observed *time.Time
	apply              func(ctx context.Context, tx *sql.Tx, sourceID, captureID int64) error
}

func (d documentAdmission) admit(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome) (int64, string, map[string]int, error) {
	registered, err := readDocumentLocator(ctx, tx, attempt.LocatorID, true)
	if err != nil {
		return 0, "", nil, err
	}
	if registered != d.binding {
		return 0, "", nil, errors.New("documents: capture binding mismatch")
	}
	var previous, previousSource int64
	var digest, qualification, coverage, encoded string
	err = tx.QueryRowContext(ctx, `select a.id,s.id,coalesce(a.content_digest,''),s.qualification,a.coverage,a.losses_json
from knowledge_locators l join knowledge_attempts a on a.id=l.last_admitted
join knowledge_sources s on s.id=a.source_id where l.id=?`, attempt.LocatorID).
		Scan(&previous, &previousSource, &digest, &qualification, &coverage, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil, err
	}
	losses := map[string]int{}
	if outcome.Status == "unchanged" {
		if previous == 0 || digest != outcome.ContentDigest || qualification != d.qualification {
			return 0, "", nil, errors.New("documents: unchanged input not previously admitted")
		}
		if err := json.Unmarshal([]byte(encoded), &losses); err != nil {
			return 0, "", nil, err
		}
	} else {
		coverage = "observations"
		if outcome.Status == "partial" {
			coverage = "partial"
		}
		for _, loss := range d.losses {
			losses[loss.code] += loss.count
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification)
values(?,'unresolved',?) on conflict(locator_id,boundary,qualification) do nothing`, attempt.LocatorID, d.qualification); err != nil {
		return 0, "", nil, err
	}
	var sourceID, frontier int64
	if err := tx.QueryRowContext(ctx, `select id,coalesce(admitted_attempt,0) from knowledge_sources
where locator_id=? and boundary='unresolved' and qualification=?`, attempt.LocatorID, d.qualification).
		Scan(&sourceID, &frontier); err != nil {
		return 0, "", nil, err
	}
	if frontier >= attempt.ID {
		return 0, "", nil, ErrStaleKnowledgeAttempt
	}
	physical := d.documents
	for _, code := range d.unmappedLosses {
		physical += losses[code]
	}
	// Membership is stored once. An unchanged capture holds none of its own: members_capture names
	// the capture whose members, and usage relations, it shares, so repeating a check of a large
	// source adds one metadata row and no row per observation. Null means the capture owns its rows.
	var members any
	if outcome.Status == "unchanged" {
		if previousSource != sourceID {
			return 0, "", nil, errors.New("documents: unchanged source mismatch")
		}
		var owner int64
		if err := tx.QueryRowContext(ctx, `select physical_observations,coalesce(members_capture,capture_id)
from document_capture_metadata where capture_id=?`, previous).Scan(&physical, &owner); err != nil {
			return 0, "", nil, err
		}
		members = owner
	}
	if _, err := tx.ExecContext(ctx, `insert into document_capture_metadata(capture_id,surface,application,
acquired_at,source_observed_at,reused,physical_observations,variant,members_capture) values(?,?,?,?,?,?,?,?,?)`,
		attempt.ID, registered.Surface, registered.Application, optionalPeopleTime(d.acquired), optionalPeopleTime(d.observed),
		outcome.Status == "unchanged", physical, registered.Variant, members); err != nil {
		return 0, "", nil, err
	}
	if outcome.Status != "unchanged" {
		if err := d.apply(ctx, tx, sourceID, attempt.ID); err != nil {
			return 0, "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,
complete_attempt=case when ?='observations' then ? else complete_attempt end,source_observed_at=? where id=?`,
		attempt.ID, coverage, attempt.ID, knowledgeTime(outcome.SourceObservedAt), sourceID); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, "update knowledge_locators set last_admitted=? where id=?", attempt.ID, attempt.LocatorID); err != nil {
		return 0, "", nil, err
	}
	return sourceID, coverage, losses, nil
}

func applyDocumentObservations(ctx context.Context, tx *sql.Tx, sourceID, captureID int64, result officedocuments.Result, application string) error {
	for _, document := range result.Documents {
		encoded, err := json.Marshal(document)
		if err != nil {
			return err
		}
		_, err = publishDocumentObservation(ctx, tx, documentPublication{sourceID: sourceID, captureID: captureID,
			digest: hashOf(application + "\x00" + string(encoded)), ordinal: document.Ordinal, variant: DocumentVariantJSON,
			surface: document.Surface, application: application, title: document.Title, url: document.URL, webURL: document.WebURL,
			modified: knowledgeTime(document.Modified.Value), documentJSON: string(encoded),
			metadata: officedocuments.MetadataText(document)})
		if err != nil {
			return err
		}
	}
	return nil
}

// documentPublication is one immutable representation with its capture membership and FTS text.
type documentPublication struct {
	sourceID, captureID                      int64
	digest, variant, application, title, url string
	webURL, documentJSON, metadata           string
	surface                                  officedocuments.Surface
	ordinal                                  int
	modified, registryJSON, opened           any
	// usageJSON and nativeKey are set only for a OneDrive usage row.
	usageJSON, nativeKey string
}

// publishDocumentObservation returns the rowid of the representation: the new row, or the
// existing one with the same source and digest, which keeps its original capture and clocks.
func publishDocumentObservation(ctx context.Context, tx *sql.Tx, p documentPublication) (int64, error) {
	var usage any
	if p.usageJSON != "" {
		usage = p.usageJSON
	}
	if _, err := tx.ExecContext(ctx, `insert into document_observations(source_id,capture_id,representation_digest,
ordinal,surface,application,title,url,web_url,modified_at,document_json,variant,registry_json,opened_at,usage_json,native_key)
values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
on conflict(source_id,representation_digest) do nothing`, p.sourceID, p.captureID, p.digest, p.ordinal,
		p.surface, p.application, p.title, p.url, p.webURL, p.modified, p.documentJSON, p.variant, p.registryJSON, p.opened,
		usage, p.nativeKey); err != nil {
		return 0, err
	}
	var rowID int64
	if err := tx.QueryRowContext(ctx, `select rowid from document_observations where source_id=? and representation_digest=?`, p.sourceID, p.digest).Scan(&rowID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into document_capture_members(capture_id,observation_rowid)
values(?,?) on conflict(capture_id,observation_rowid) do nothing`, p.captureID, rowID); err != nil {
		return 0, err
	}
	var indexed int
	if err := tx.QueryRowContext(ctx, `select count(*) from document_fts where rowid=?`, rowID).Scan(&indexed); err != nil {
		return 0, err
	}
	if indexed == 0 {
		if _, err := tx.ExecContext(ctx, `insert into document_fts(rowid,title,metadata) values(?,?,?)`,
			rowID, p.title, p.metadata); err != nil {
			return 0, err
		}
	}
	return rowID, nil
}
