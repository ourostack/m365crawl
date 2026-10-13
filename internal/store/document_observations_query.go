package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func (s *Store) DocumentSources(ctx context.Context) ([]KnowledgeSource, error) {
	return readDocumentSources(ctx, s.db)
}

func readDocumentSources(ctx context.Context, reader knowledgeReader) ([]KnowledgeSource, error) {
	available, variants, err := documentSchema(ctx, reader)
	if err != nil || !available {
		return []KnowledgeSource{}, err
	}
	kinds := []string{DocumentKind(officedocuments.Recent), DocumentKind(officedocuments.Shared),
		DocumentKind(officedocuments.Recommended), DocumentKind(officedocuments.Dialog)}
	if variants {
		kinds = append(kinds, RegistryDocumentKind)
	}
	out := []KnowledgeSource{}
	for _, kind := range kinds {
		sources, err := readKnowledgeSources(ctx, reader, kind)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	slices.SortFunc(out, func(a, b KnowledgeSource) int {
		if a.LocatorID != b.LocatorID {
			return cmp.Compare(a.LocatorID, b.LocatorID)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (s *Store) DocumentLocators(ctx context.Context, sourceID int64) ([]KnowledgeSource, error) {
	sources, err := s.DocumentSources(ctx)
	if err != nil {
		return nil, err
	}
	return documentLocators(sources, sourceID)
}

func documentLocators(sources []KnowledgeSource, sourceID int64) ([]KnowledgeSource, error) {
	if sourceID < 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	var selected int64
	if sourceID > 0 {
		for _, source := range sources {
			if source.ID == sourceID {
				selected = source.LocatorID
				break
			}
		}
		if selected == 0 {
			return nil, ErrKnowledgeSourceUnknown
		}
	}
	out := []KnowledgeSource{}
	seen := map[int64]bool{}
	for _, source := range sources {
		if source.Status != "retained" && !seen[source.LocatorID] &&
			(sourceID == 0 || source.LocatorID == selected) {
			out = append(out, source)
			seen[source.LocatorID] = true
		}
	}
	if sourceID > 0 && len(out) == 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	return out, nil
}

func (s *Store) DocumentSnapshot(ctx context.Context, filter DocumentFilter) (DocumentObservationSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentObservationSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := readDocumentSnapshot(ctx, tx, filter)
	if err != nil {
		return DocumentObservationSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentObservationSnapshot{}, err
	}
	return snapshot, nil
}

func readDocumentSnapshot(ctx context.Context, reader knowledgeReader, filter DocumentFilter) (DocumentObservationSnapshot, error) {
	sources, err := readDocumentSources(ctx, reader)
	if err != nil {
		return DocumentObservationSnapshot{}, err
	}
	if filter.SourceID != 0 {
		if _, err := documentLocators(sources, filter.SourceID); err != nil {
			return DocumentObservationSnapshot{}, err
		}
	}
	selected := []KnowledgeSource{}
	for _, source := range sources {
		if filter.SourceID == 0 || source.ID == filter.SourceID {
			selected = append(selected, source)
		}
	}
	rows, truncated, err := readDocumentObservations(ctx, reader, filter, 0)
	if err != nil {
		return DocumentObservationSnapshot{}, err
	}
	return DocumentObservationSnapshot{Rows: rows, Sources: selected, Truncated: truncated}, nil
}

func (s *Store) DocumentObservations(ctx context.Context, filter DocumentFilter) ([]DocumentObservationRow, bool, error) {
	if filter.SourceID != 0 {
		if _, err := s.DocumentLocators(ctx, filter.SourceID); err != nil {
			return nil, false, err
		}
	}
	return readDocumentObservations(ctx, s.db, filter, 0)
}

func readDocumentObservations(ctx context.Context, reader knowledgeReader, filter DocumentFilter, id int64) ([]DocumentObservationRow, bool, error) {
	if filter.Limit < 0 || filter.Limit == math.MaxInt {
		return nil, false, errors.New("documents: invalid limit")
	}
	available, variants, err := documentSchema(ctx, reader)
	if err != nil || !available {
		return []DocumentObservationRow{}, false, err
	}
	// An archive from before schema 14 has no variant columns: every row is Office JSON.
	variantColumns := "'" + DocumentVariantJSON + "',null,null"
	if variants {
		variantColumns = "d.variant,d.registry_json,d.opened_at"
	}
	where := "1=1"
	args := []any{}
	if id > 0 {
		where += " and d.rowid=?"
		args = append(args, id)
	}
	if filter.SourceID > 0 {
		where += " and d.source_id=?"
		args = append(args, filter.SourceID)
	}
	if filter.Application != "" {
		where += " and d.application=?"
		args = append(args, filter.Application)
	}
	if filter.Surface != "" {
		where += " and d.surface=?"
		args = append(args, filter.Surface)
	}
	if strings.HasPrefix(filter.Query, "document-observation:") {
		queryID, err := strconv.ParseInt(strings.TrimPrefix(filter.Query, "document-observation:"), 10, 64)
		if err != nil || queryID <= 0 {
			return nil, false, errs.Usage("documents query has an invalid observation ID")
		}
		where += " and d.rowid=?"
		args = append(args, queryID)
	} else if filter.Query != "" {
		match := buildFTSQuery(filter.Query)
		if match == "" {
			return nil, false, errs.Usage("documents query requires searchable text")
		}
		where += " and d.rowid in (select rowid from document_fts where document_fts match ?)"
		args = append(args, match)
	}
	if !filter.ModifiedSince.IsZero() {
		where += " and d.modified_at>=?"
		args = append(args, knowledgeTime(filter.ModifiedSince))
	}
	if !filter.ModifiedUntil.IsZero() {
		where += " and d.modified_at<?"
		args = append(args, knowledgeTime(filter.ModifiedUntil))
	}
	limit := ""
	if filter.Limit > 0 {
		limit = " limit ?"
		args = append(args, filter.Limit+1)
	}
	rows, err := reader.QueryContext(ctx, `select d.rowid,d.source_id,d.capture_id,d.document_json,d.application,d.surface,
c.acquired_at,c.source_observed_at,s.id,`+variantColumns+`,coalesce(l.kind,''),
not exists(select 1 from document_capture_members m where m.capture_id=s.admitted_attempt and m.observation_rowid=d.rowid)
or not exists(select 1 from knowledge_attempts a join knowledge_locators l on l.last_admitted=a.id where l.id=s.locator_id and a.source_id=d.source_id)
from document_observations d join document_capture_metadata c on c.capture_id=d.capture_id
left join knowledge_sources s on s.id=d.source_id
left join knowledge_locators l on l.id=s.locator_id where `+where+`
order by d.modified_at desc,d.title collate nocase,d.source_id,d.rowid`+limit, args...)
	if err != nil {
		return nil, false, err
	}
	out := []DocumentObservationRow{}
	for rows.Next() {
		var row DocumentObservationRow
		var stored documentStored
		var acquired, observed sql.NullString
		var linkedSource sql.NullInt64
		if err := rows.Scan(&row.ID, &row.SourceID, &row.CaptureID, &stored.document, &row.Application,
			&stored.surface, &acquired, &observed, &linkedSource, &row.Variant, &stored.registry, &stored.opened,
			&stored.kind, &row.Retained); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		if !linkedSource.Valid {
			_ = rows.Close()
			return nil, false, ErrKnowledgeSourceUnknown
		}
		if err := decodeDocumentRow(&row, stored, acquired, observed); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		out = append(out, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	truncated := filter.Limit > 0 && len(out) > filter.Limit
	if truncated {
		out = out[:filter.Limit]
	}
	return out, truncated, nil
}

// documentStored is a stored row's raw discriminated payload, before validation.
type documentStored struct {
	document, kind   string
	surface          officedocuments.Surface
	registry, opened sql.NullString
}

// decodeDocumentRow validates the discriminator, kind and payload combination of every row it
// returns: a corrupt combination is an error, never an empty success.
func decodeDocumentRow(row *DocumentObservationRow, stored documentStored, acquired, observed sql.NullString) error {
	switch row.Variant {
	case DocumentVariantJSON:
		if stored.registry.Valid || stored.opened.Valid {
			return errors.New("documents: invalid stored representation")
		}
		if err := json.Unmarshal([]byte(stored.document), &row.Document); err != nil {
			return err
		}
		if row.Document.Surface != stored.surface || !validDocumentBinding(stored.surface, row.Application) {
			return errors.New("documents: invalid stored representation")
		}
	case RegistryDocumentKind:
		if err := decodeRegistryRow(row, stored); err != nil {
			return err
		}
	default:
		return errors.New("documents: invalid stored variant")
	}
	times, err := knowledgeTimes(acquired, observed)
	if err != nil {
		return err
	}
	row.CapturedAt, row.SourceObservedAt = times[0], times[1]
	return nil
}

const documentReferencesSQL = `select rowid as observation_id,source_id,capture_id,'url' as field,url as reference_url,application,surface
from document_observations where url<>'' and (url=? or url=?)
union all
select rowid as observation_id,source_id,capture_id,'web_url' as field,web_url as reference_url,application,surface
from document_observations where web_url<>'' and (web_url=? or web_url=?)`

// documentPathReferencesSQL adds the literal FriendlyPath occurrences among registry rows. The
// path is compared as stored text; it is never normalized, resolved or looked up on disk.
const documentPathReferencesSQL = `
union all
select rowid as observation_id,source_id,capture_id,'friendly_path' as field,json_extract(registry_json,'$.FriendlyPath') as reference_url,application,surface
from document_observations where variant='office_registry' and ?<>'' and json_extract(registry_json,'$.FriendlyPath')=?`

func (s *Store) DocumentObservation(ctx context.Context, id int64) (DocumentObservationDetail, error) {
	if id <= 0 {
		return DocumentObservationDetail{}, ErrDocumentObservationNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DocumentObservationDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, _, err := readDocumentObservations(ctx, tx, DocumentFilter{Limit: 1}, id)
	if err != nil {
		return DocumentObservationDetail{}, err
	}
	if len(rows) == 0 {
		return DocumentObservationDetail{}, ErrDocumentObservationNotFound
	}
	detail := DocumentObservationDetail{Observation: rows[0], References: []DocumentReferenceObservation{}}
	kind, urls, path := DocumentKind(detail.Observation.Document.Surface),
		[]string{detail.Observation.Document.URL, detail.Observation.Document.WebURL}, ""
	referencesSQL := documentReferencesSQL
	if detail.Observation.Variant == RegistryDocumentKind {
		kind, urls, path = RegistryDocumentKind, []string{detail.Observation.Registry.URL, ""}, detail.Observation.Registry.FriendlyPath
	}
	args := []any{urls[0], urls[1], urls[0], urls[1]}
	if _, variants, err := documentSchema(ctx, tx); err != nil {
		return DocumentObservationDetail{}, err
	} else if variants {
		referencesSQL += documentPathReferencesSQL
		args = append(args, path, path)
	}
	sources, err := readKnowledgeSources(ctx, tx, kind)
	if err != nil {
		return DocumentObservationDetail{}, err
	}
	for _, source := range sources {
		if source.ID == detail.Observation.SourceID {
			detail.Source = source
			break
		}
	}
	if detail.Source.ID == 0 {
		return DocumentObservationDetail{}, ErrKnowledgeSourceUnknown
	}
	if err := tx.QueryRowContext(ctx, `select count(*) from (`+referencesSQL+`)`, args...).
		Scan(&detail.ReferencesTotal); err != nil {
		return DocumentObservationDetail{}, err
	}
	references, err := tx.QueryContext(ctx, `select * from (`+referencesSQL+`) order by observation_id,field limit 100`, args...)
	if err != nil {
		return DocumentObservationDetail{}, err
	}
	for references.Next() {
		var reference DocumentReferenceObservation
		if err := references.Scan(&reference.ObservationID, &reference.SourceID, &reference.CaptureID,
			&reference.Field, &reference.URL, &reference.Application, &reference.Surface); err != nil {
			_ = references.Close()
			return DocumentObservationDetail{}, err
		}
		reference.AssociationState = "unresolved"
		detail.References = append(detail.References, reference)
	}
	if err := errors.Join(references.Err(), references.Close()); err != nil {
		return DocumentObservationDetail{}, err
	}
	detail.ReferencesTruncated = detail.ReferencesTotal > len(detail.References)
	if err := tx.Commit(); err != nil {
		return DocumentObservationDetail{}, err
	}
	return detail, nil
}
