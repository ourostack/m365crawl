package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

func hasCloudSchema(ctx context.Context, reader knowledgeReader) (bool, error) {
	var count int
	err := reader.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in
('cloud_locator_metadata','cloud_observations','cloud_capture_members','cloud_capture_metadata','cloud_source_body_state','cloud_fts')`).Scan(&count)
	return count == 6, err
}

func readCloudRequest(ctx context.Context, reader knowledgeReader, id int64) (sharepointcontent.CanonicalRequest, error) {
	var request sharepointcontent.CanonicalRequest
	var encoded, kind, platform, locator string
	err := reader.QueryRowContext(ctx, `select m.request_json,l.kind,l.platform,l.locator from cloud_locator_metadata m
join knowledge_locators l on l.id=m.locator_id where m.locator_id=? and octet_length(m.request_json)<=?`, id, 2<<20).Scan(&encoded, &kind, &platform, &locator)
	if errors.Is(err, sql.ErrNoRows) {
		return request, ErrKnowledgeSourceUnknown
	}
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		return request, errors.New("cloud: corrupt request")
	}
	canonical, err := sharepointcontent.NormalizeRequest(request.Request)
	expected, _ := cloudKind(request)
	if err != nil || canonical != request || kind != expected || platform != "browser" || locator != request.LocatorKey {
		return sharepointcontent.CanonicalRequest{}, errors.New("cloud: corrupt registration")
	}
	return request, nil
}

func (s *Store) CloudRequest(ctx context.Context, registrationID int64) (sharepointcontent.CanonicalRequest, error) {
	ok, err := hasCloudSchema(ctx, s.db)
	if err != nil {
		return sharepointcontent.CanonicalRequest{}, err
	}
	if !ok || registrationID <= 0 {
		return sharepointcontent.CanonicalRequest{}, ErrKnowledgeSourceUnknown
	}
	return readCloudRequest(ctx, s.db, registrationID)
}

const cloudObservationSelect = `select o.rowid,o.source_id,o.capture_id,
case when octet_length(o.result_json)<=16777216 then o.result_json else '' end,
c.completed_at,l.last_admitted is not s.admitted_attempt or not exists(
select 1 from cloud_capture_members m where m.capture_id=s.admitted_attempt and m.observation_rowid=o.rowid),
o.representation_digest,o.kind,o.title,o.file_modified_at,s.qualification,s.boundary,
a.source_id,a.locator_id,a.content_digest,a.finished_at,
exists(select 1 from cloud_capture_members original where original.capture_id=o.capture_id and original.observation_rowid=o.rowid),
s.locator_id
from cloud_observations o join knowledge_sources s on s.id=o.source_id
join knowledge_locators l on l.id=s.locator_id
left join cloud_capture_metadata c on c.capture_id=o.capture_id
left join knowledge_attempts a on a.id=o.capture_id`

type cloudObservationBinding struct {
	row                                                   CloudObservationRow
	encoded, clock, digest, kind, qualification, boundary string
	title, modified                                       sql.NullString
	captureSourceID, captureLocatorID, locatorID          int64
	captureDigest, finished                               string
	originalMember                                        bool
}

func scanCloudObservation(scanner interface{ Scan(...any) error }) (cloudObservationBinding, error) {
	var binding cloudObservationBinding
	err := scanner.Scan(&binding.row.ID, &binding.row.SourceID, &binding.row.CaptureID,
		&binding.encoded, &binding.clock, &binding.row.Retained, &binding.digest, &binding.kind,
		&binding.title, &binding.modified, &binding.qualification, &binding.boundary,
		&binding.captureSourceID, &binding.captureLocatorID, &binding.captureDigest, &binding.finished,
		&binding.originalMember, &binding.locatorID)
	return binding, err
}

func validateCloudObservation(ctx context.Context, request sharepointcontent.CanonicalRequest, registrationID int64, binding cloudObservationBinding) (CloudObservationRow, error) {
	row := binding.row
	if err := json.Unmarshal([]byte(binding.encoded), &row.Result); err != nil {
		return row, errors.New("cloud: corrupt observation")
	}
	at, err := time.Parse(time.RFC3339Nano, binding.clock)
	if err != nil {
		return row, errors.New("cloud: corrupt capture clock")
	}
	row.CapturedAt = at
	if err := validateCloudCapture(ctx, CloudCapture{request, row.Result, at}); err != nil {
		return row, err
	}
	expectedKind, qualification := cloudKind(request)
	title, modified := "", ""
	if row.Result.File.Title != nil {
		title = *row.Result.File.Title
	}
	if row.Result.File.ModifiedAt != nil {
		modified = knowledgeTime(*row.Result.File.ModifiedAt).(string)
	}
	if binding.kind != expectedKind || binding.qualification != qualification ||
		binding.digest != hashOf(qualification+binding.encoded) ||
		binding.title.String != title || binding.title.Valid != (row.Result.File.Title != nil) ||
		binding.modified.String != modified || binding.modified.Valid != (row.Result.File.ModifiedAt != nil) ||
		binding.boundary != cloudBoundary(expectedKind, row.Result) {
		return row, errors.New("cloud: corrupt observation binding")
	}
	if binding.captureSourceID != row.SourceID || binding.captureLocatorID != registrationID ||
		binding.locatorID != registrationID || binding.captureDigest != binding.digest ||
		binding.finished != binding.clock || !binding.originalMember {
		return row, errors.New("cloud: corrupt observation capture")
	}
	return row, nil
}

func readCloudObservation(ctx context.Context, reader knowledgeReader, request sharepointcontent.CanonicalRequest, registrationID, sourceID, id int64) (CloudObservationRow, error) {
	binding, err := scanCloudObservation(reader.QueryRowContext(ctx, cloudObservationSelect+` where o.rowid=? and o.source_id=?`, id, sourceID))
	if err != nil {
		return CloudObservationRow{}, err
	}
	return validateCloudObservation(ctx, request, registrationID, binding)
}

func readCloudSources(ctx context.Context, reader knowledgeReader, sourceID int64) ([]CloudSource, error) {
	ok, err := hasCloudSchema(ctx, reader)
	if err != nil {
		return nil, err
	}
	if !ok {
		if sourceID != 0 {
			return nil, ErrKnowledgeSourceUnknown
		}
		return []CloudSource{}, nil
	}
	out := []CloudSource{}
	for _, kind := range []string{CloudPageKind, CloudStreamKind} {
		sources, err := readKnowledgeSources(ctx, reader, kind)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			if sourceID != 0 && source.ID != sourceID {
				continue
			}
			request, err := readCloudRequest(ctx, reader, source.LocatorID)
			if err != nil {
				return nil, err
			}
			item := CloudSource{Source: source, RegistrationID: source.LocatorID, SelectionMatch: "unavailable"}
			if request.Request.TranscriptID != "" {
				value := request.Request.TranscriptID
				item.RequestedTranscriptID = &value
			}
			if source.ID != 0 {
				var body sql.NullInt64
				var selected sql.NullString
				if err := reader.QueryRowContext(ctx, `select last_good_observation_id,current_transcript_id from cloud_source_body_state where source_id=?`, source.ID).Scan(&body, &selected); err != nil {
					return nil, err
				}
				if body.Valid {
					value := body.Int64
					item.LastGoodObservationID = &value
				}
				if selected.Valid {
					value := selected.String
					item.CurrentTranscriptID = &value
				}
				var requested, current sql.NullString
				var currentBody sql.NullInt64
				var currentObservationID int64
				var currentDigest string
				if err := reader.QueryRowContext(ctx, `select c.requested_transcript_id,c.current_transcript_id,c.body_observation_id,o.rowid,o.representation_digest
from cloud_capture_metadata c join cloud_capture_members m on m.capture_id=c.capture_id
join cloud_observations o on o.rowid=m.observation_rowid and o.source_id=?
where c.capture_id=? and octet_length(o.result_json)<=?`, source.ID, source.AdmittedID, cloudJSONLimit).Scan(&requested, &current, &currentBody, &currentObservationID, &currentDigest); err != nil {
					return nil, err
				}
				if requested.String != request.Request.TranscriptID || requested.Valid != (request.Request.TranscriptID != "") || selected != current {
					return nil, errors.New("cloud: corrupt capture selection")
				}
				currentRow, err := readCloudObservation(ctx, reader, request, source.LocatorID, source.ID, currentObservationID)
				if err != nil {
					return nil, err
				}
				currentResult := currentRow.Result
				expectedKind, qual := cloudKind(request)
				if source.Qualification != qual || source.Boundary != cloudBoundary(expectedKind, currentResult) || source.ContentDigest != currentDigest {
					return nil, errors.New("cloud: corrupt current binding")
				}
				hasBody := currentResult.State == "page_observations" || currentResult.Transcript != nil
				if currentBody.Valid != hasBody || (hasBody && (!body.Valid || body.Int64 != currentBody.Int64 || currentBody.Int64 != currentRow.ID)) ||
					(currentResult.Transcript != nil && (!selected.Valid || selected.String != currentResult.Transcript.ID)) ||
					(currentResult.Transcript == nil && selected.Valid) {
					return nil, errors.New("cloud: corrupt body frontier")
				}
				if body.Valid {
					row, err := readCloudObservation(ctx, reader, request, source.LocatorID, source.ID, body.Int64)
					if err != nil {
						return nil, err
					}
					result := row.Result
					if result.Transcript == nil && result.State != "page_observations" {
						return nil, errors.New("cloud: invalid body selection")
					}
					if result.Transcript != nil && selected.Valid {
						item.SelectionMatch = "differs"
						if result.Transcript.ID == selected.String {
							item.SelectionMatch = "matches"
						}
					}
				}
			}
			out = append(out, item)
		}
	}
	if sourceID != 0 && len(out) == 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RegistrationID != out[j].RegistrationID {
			return out[i].RegistrationID < out[j].RegistrationID
		}
		return out[i].Source.ID < out[j].Source.ID
	})
	return out, nil
}

func (s *Store) CloudSources(ctx context.Context, sourceID int64) ([]CloudSource, error) {
	if sourceID < 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readCloudSources(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// cloudReader is a read-transaction seam for interleaved publication contracts.
var cloudReader = func(tx *sql.Tx) knowledgeReader { return tx }

func (s *Store) CloudSnapshot(ctx context.Context, filter CloudFilter) (CloudSnapshot, error) {
	return s.cloudSnapshot(ctx, filter, 0)
}
func (s *Store) cloudSnapshot(ctx context.Context, filter CloudFilter, id int64) (CloudSnapshot, error) {
	if filter.SourceID < 0 || filter.Limit < 0 || filter.Limit == math.MaxInt {
		return CloudSnapshot{}, errors.New("cloud: invalid filter")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CloudSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	reader := cloudReader(tx)
	sources, err := readCloudSources(ctx, reader, filter.SourceID)
	if err != nil {
		return CloudSnapshot{}, err
	}
	out := CloudSnapshot{Rows: []CloudObservationRow{}, Sources: sources}
	ok, err := hasCloudSchema(ctx, reader)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, tx.Commit()
	}
	where := "1=1"
	args := []any{}
	if filter.SourceID > 0 {
		where += " and o.source_id=?"
		args = append(args, filter.SourceID)
	}
	if id > 0 {
		where += " and o.rowid=?"
		args = append(args, id)
	}
	if filter.Query != "" {
		match := buildFTSQuery(filter.Query)
		if match == "" {
			return out, errs.Usage("content search requires searchable words")
		}
		where += " and o.rowid in (select rowid from cloud_fts where cloud_fts match ?)"
		args = append(args, match)
	}
	if !filter.ModifiedSince.IsZero() {
		where += " and o.file_modified_at>=?"
		args = append(args, knowledgeTime(filter.ModifiedSince))
	}
	if !filter.ModifiedUntil.IsZero() {
		where += " and o.file_modified_at<=?"
		args = append(args, knowledgeTime(filter.ModifiedUntil))
	}
	limit := ""
	if filter.Limit > 0 {
		limit = " limit ?"
		args = append(args, filter.Limit+1)
	}
	rows, err := reader.QueryContext(ctx, cloudObservationSelect+` where `+where+`
order by o.file_modified_at is null,o.file_modified_at desc,coalesce(o.title,''),o.source_id,o.rowid`+limit, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		binding, err := scanCloudObservation(rows)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		row := binding.row
		var src *CloudSource
		for i := range sources {
			if sources[i].Source.ID == row.SourceID {
				src = &sources[i]
				break
			}
		}
		if src == nil {
			_ = rows.Close()
			return out, errors.New("cloud: missing observation source")
		}
		request, err := readCloudRequest(ctx, reader, src.RegistrationID)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		row, err = validateCloudObservation(ctx, request, src.RegistrationID, binding)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Rows = append(out.Rows, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	if filter.Limit > 0 && len(out.Rows) > filter.Limit {
		out.Truncated = true
		out.Rows = out.Rows[:filter.Limit]
	}
	return out, tx.Commit()
}

func (s *Store) CloudDetail(ctx context.Context, id int64) (CloudObservationRow, CloudSource, error) {
	if id <= 0 {
		return CloudObservationRow{}, CloudSource{}, ErrKnowledgeSourceUnknown
	}
	snapshot, err := s.cloudSnapshot(ctx, CloudFilter{}, id)
	if err != nil {
		return CloudObservationRow{}, CloudSource{}, err
	}
	if len(snapshot.Rows) != 1 {
		return CloudObservationRow{}, CloudSource{}, ErrKnowledgeSourceUnknown
	}
	row := snapshot.Rows[0]
	source := CloudSource{}
	for _, candidate := range snapshot.Sources {
		if candidate.Source.ID == row.SourceID {
			source = candidate
			break
		}
	}
	source.SelectionMatch = "unavailable"
	if row.Result.Transcript != nil && source.CurrentTranscriptID != nil {
		source.SelectionMatch = "differs"
		if strings.Compare(row.Result.Transcript.ID, *source.CurrentTranscriptID) == 0 {
			source.SelectionMatch = "matches"
		}
	}
	return row, source, nil
}
