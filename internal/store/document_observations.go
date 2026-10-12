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
)

const DocumentQualification = "office-json-v1/mapper-v1"

var ErrDocumentObservationNotFound = errors.New("documents: observation not found")

type DocumentCaptureMetadata struct {
	Surface                      officedocuments.Surface
	Application                  string
	AcquiredAt, SourceObservedAt *time.Time
}

type DocumentObservationRow struct {
	ID, SourceID, CaptureID      int64
	Document                     officedocuments.Document
	Application                  string
	CapturedAt, SourceObservedAt *time.Time
	Retained                     bool
}

type DocumentObservationSnapshot struct {
	Rows      []DocumentObservationRow
	Sources   []KnowledgeSource
	Truncated bool
}

type DocumentFilter struct {
	Query                        string
	SourceID                     int64
	ModifiedSince, ModifiedUntil time.Time
	Limit                        int
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

func (s *Store) BeginDocumentAttempt(ctx context.Context, input KnowledgeInput, surface officedocuments.Surface, application string) (KnowledgeAttempt, error) {
	if !validDocumentBinding(surface, application) || input.Kind != DocumentKind(surface) {
		return KnowledgeAttempt{}, errors.New("documents: invalid source binding")
	}
	return s.beginKnowledgeAttempt(ctx, input, func(tx *sql.Tx, locatorID int64) error {
		if _, err := tx.ExecContext(ctx, `insert into document_locator_metadata(locator_id,surface,application)
values(?,?,?) on conflict(locator_id) do nothing`, locatorID, surface, application); err != nil {
			return err
		}
		registeredSurface, registeredApp, err := readDocumentLocator(ctx, tx, locatorID)
		if err != nil {
			return err
		}
		if registeredSurface != surface || registeredApp != application {
			return errors.New("documents: source binding changed")
		}
		return nil
	})
}

func readDocumentLocator(ctx context.Context, reader knowledgeReader, id int64) (officedocuments.Surface, string, error) {
	var surface officedocuments.Surface
	var application, kind string
	err := reader.QueryRowContext(ctx, `select d.surface,d.application,l.kind
from document_locator_metadata d join knowledge_locators l on l.id=d.locator_id where d.locator_id=?`, id).
		Scan(&surface, &application, &kind)
	if err != nil {
		return "", "", err
	}
	if !validDocumentBinding(surface, application) || kind != DocumentKind(surface) {
		return "", "", errors.New("documents: invalid stored source binding")
	}
	return surface, application, nil
}

func (s *Store) DocumentLocatorInput(ctx context.Context, id int64) (officedocuments.Surface, string, error) {
	available, err := hasDocumentObservations(ctx, s.db)
	if err != nil {
		return "", "", err
	}
	if !available || id <= 0 {
		return "", "", ErrKnowledgeSourceUnknown
	}
	return readDocumentLocator(ctx, s.db, id)
}

func hasDocumentObservations(ctx context.Context, reader knowledgeReader) (bool, error) {
	var count int
	err := reader.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table'
and name in ('document_observations','document_capture_members','document_capture_metadata','document_fts','document_locator_metadata')`).Scan(&count)
	return count == 5, err
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
	return s.finishKnowledge(ctx, attempt, outcome, DocumentKind(capture.Surface),
		func(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome) (int64, string, map[string]int, error) {
			return admitDocumentObservations(ctx, tx, attempt, outcome, result, capture)
		})
}

func admitDocumentObservations(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome, result officedocuments.Result, capture DocumentCaptureMetadata) (int64, string, map[string]int, error) {
	surface, application, err := readDocumentLocator(ctx, tx, attempt.LocatorID)
	if err != nil {
		return 0, "", nil, err
	}
	if surface != capture.Surface || application != capture.Application {
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
		if previous == 0 || digest != outcome.ContentDigest || qualification != DocumentQualification {
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
		for _, loss := range result.Losses {
			losses[loss.Code] += loss.Count
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification)
values(?,'unresolved',?) on conflict(locator_id,boundary,qualification) do nothing`, attempt.LocatorID, DocumentQualification); err != nil {
		return 0, "", nil, err
	}
	var sourceID, frontier int64
	if err := tx.QueryRowContext(ctx, `select id,coalesce(admitted_attempt,0) from knowledge_sources
where locator_id=? and boundary='unresolved' and qualification=?`, attempt.LocatorID, DocumentQualification).
		Scan(&sourceID, &frontier); err != nil {
		return 0, "", nil, err
	}
	if frontier >= attempt.ID {
		return 0, "", nil, ErrStaleKnowledgeAttempt
	}
	physical := len(result.Documents) + losses["office_document_unmapped"]
	if outcome.Status == "unchanged" {
		if previousSource != sourceID {
			return 0, "", nil, errors.New("documents: unchanged source mismatch")
		}
		if err := tx.QueryRowContext(ctx, `select physical_observations from document_capture_metadata where capture_id=?`, previous).Scan(&physical); err != nil {
			return 0, "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into document_capture_metadata(capture_id,surface,application,
acquired_at,source_observed_at,reused,physical_observations) values(?,?,?,?,?,?,?)`,
		attempt.ID, surface, application, optionalPeopleTime(capture.AcquiredAt), optionalPeopleTime(capture.SourceObservedAt),
		outcome.Status == "unchanged", physical); err != nil {
		return 0, "", nil, err
	}
	if outcome.Status == "unchanged" {
		if _, err := tx.ExecContext(ctx, `insert into document_capture_members(capture_id,observation_rowid)
select ?,observation_rowid from document_capture_members where capture_id=?`, attempt.ID, previous); err != nil {
			return 0, "", nil, err
		}
	} else if err := applyDocumentObservations(ctx, tx, sourceID, attempt.ID, result, application); err != nil {
		return 0, "", nil, err
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
		digest := hashOf(application + "\x00" + string(encoded))
		if _, err := tx.ExecContext(ctx, `insert into document_observations(source_id,capture_id,representation_digest,
ordinal,surface,application,title,url,web_url,modified_at,document_json) values(?,?,?,?,?,?,?,?,?,?,?)
on conflict(source_id,representation_digest) do nothing`, sourceID, captureID, digest, document.Ordinal,
			document.Surface, application, document.Title, document.URL, document.WebURL, knowledgeTime(document.Modified.Value), string(encoded)); err != nil {
			return err
		}
		var rowID int64
		if err := tx.QueryRowContext(ctx, `select rowid from document_observations where source_id=? and representation_digest=?`, sourceID, digest).Scan(&rowID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into document_capture_members(capture_id,observation_rowid)
values(?,?) on conflict(capture_id,observation_rowid) do nothing`, captureID, rowID); err != nil {
			return err
		}
		var indexed int
		if err := tx.QueryRowContext(ctx, `select count(*) from document_fts where rowid=?`, rowID).Scan(&indexed); err != nil {
			return err
		}
		if indexed == 0 {
			if _, err := tx.ExecContext(ctx, `insert into document_fts(rowid,title,metadata) values(?,?,?)`,
				rowID, document.Title, officedocuments.MetadataText(document)); err != nil {
				return err
			}
		}
	}
	return nil
}
