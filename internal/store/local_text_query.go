package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/officecapture"
)

func hasLocalTextSchema(ctx context.Context, r knowledgeReader) (bool, error) {
	var count int
	err := r.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in
('local_text_locator_metadata','local_file_snapshots','local_text_observations','local_text_capture_members','local_text_source_state','local_text_attempt_metadata','local_text_references','local_text_fts')`).Scan(&count)
	if err == nil && count != 0 && count != 8 {
		return false, errors.New("local text: incomplete schema")
	}
	if err == nil && count == 0 {
		var version int
		if err := r.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
			return false, err
		}
		if version >= 17 {
			return false, errors.New("local text: missing current schema")
		}
	}
	return count == 8, err
}

func readLocalRequest(ctx context.Context, r knowledgeReader, id int64) (officecapture.TextInput, string, error) {
	var input officecapture.TextInput
	var platform, locator, kind string
	err := r.QueryRowContext(ctx, `select m.path,m.requested_kind,l.platform,l.locator,l.kind
from local_text_locator_metadata m join knowledge_locators l on l.id=m.locator_id
where m.locator_id=? and octet_length(m.path)<=32768 and octet_length(l.locator)<=262144`, id).
		Scan(&input.Path, &input.RequestedKind, &platform, &locator, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return input, "", ErrKnowledgeSourceUnknown
	}
	if err != nil {
		return input, "", err
	}
	if !officecapture.ValidTextRegistration(input, platform) || LocalTextLocator(input) != locator || kind != LocalTextKind ||
		(platform != "darwin" && platform != "windows") {
		return input, "", errors.New("local text: corrupt registration")
	}
	return input, platform, nil
}

func (s *Store) LocalTextRequest(ctx context.Context, id int64) (officecapture.TextInput, error) {
	ok, err := hasLocalTextSchema(ctx, s.db)
	if err != nil {
		return officecapture.TextInput{}, err
	}
	if !ok || id <= 0 {
		return officecapture.TextInput{}, ErrKnowledgeSourceUnknown
	}
	input, _, err := readLocalRequest(ctx, s.db, id)
	return input, err
}

const localObservationSelect = `select o.rowid,o.source_id,o.capture_id,o.snapshot_id,o.representation_digest,
o.requested_kind,o.verified_kind,
case when octet_length(o.result_json)<=33554432 then o.result_json else '' end,o.file_modified_at,
n.source_id,n.capture_id,n.source_byte_digest,
case when octet_length(n.evidence_json)<=65536 then n.evidence_json else '' end,
n.qualification,n.acquired_at,n.captured_at,a.finished_at,
a.source_id,a.locator_id,a.content_digest,s.locator_id,s.boundary,s.qualification,
exists(select 1 from local_text_capture_members m where m.capture_id=o.capture_id and m.observation_rowid=o.rowid),
am.request_origin,na.source_id,na.locator_id,na.finished_at,
exists(select 1 from local_text_capture_members m join local_text_observations original on original.rowid=m.observation_rowid
where m.capture_id=n.capture_id and original.snapshot_id=n.rowid and original.source_id=n.source_id)
from local_text_observations o left join local_file_snapshots n on n.rowid=o.snapshot_id
left join knowledge_sources s on s.id=o.source_id left join knowledge_attempts a on a.id=o.capture_id
left join knowledge_attempts na on na.id=n.capture_id
left join local_text_attempt_metadata am on am.attempt_id=o.capture_id`

func validateLocalTextRow(ctx context.Context, row LocalTextRow) error {
	if row.ID <= 0 || row.SourceID <= 0 || row.CaptureID <= 0 || row.Snapshot.ID <= 0 || row.Snapshot.SourceID != row.SourceID ||
		row.Snapshot.Qualification != LocalTextQualification || row.CapturedAt.IsZero() || row.Snapshot.CapturedAt.IsZero() ||
		row.Snapshot.AcquiredAt.After(row.Snapshot.CapturedAt) {
		return errors.New("local text: invalid row")
	}
	return validateLocalTextCommit(ctx, LocalTextCommit{Capture: row.Capture, RequestOrigin: "explicit"})
}

func readLocalRows(ctx context.Context, r knowledgeReader) ([]LocalTextRow, error) {
	rows, err := r.QueryContext(ctx, localObservationSelect+` order by o.rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []LocalTextRow{}
	// Registration reads happen only after closing the row cursor (also safe with one connection).
	type binding struct {
		locator                 int64
		boundary, qualification string
	}
	bindings := []binding{}
	for rows.Next() {
		var row LocalTextRow
		var verified, modified sql.NullString
		var result, evidence, acquired, snapshotClock, captured, boundary, qual, origin, nativeFinished string
		var captureSource, captureLocator, locator, nativeSource, nativeLocator int64
		var captureDigest string
		var member, nativeMember bool
		if err := rows.Scan(&row.ID, &row.SourceID, &row.CaptureID, &row.Snapshot.ID, &row.RepresentationDigest,
			&row.Capture.RequestedKind, &verified, &result, &modified, &row.Snapshot.SourceID, &row.Snapshot.CaptureID,
			&row.Snapshot.SourceByteDigest, &evidence, &row.Snapshot.Qualification, &acquired, &snapshotClock, &captured,
			&captureSource, &captureLocator, &captureDigest, &locator, &boundary, &qual, &member, &origin,
			&nativeSource, &nativeLocator, &nativeFinished, &nativeMember); err != nil {
			return nil, err
		}
		if verified.Valid {
			value := verified.String
			row.Capture.VerifiedKind = &value
		}
		if err := json.Unmarshal([]byte(result), &row.Capture.Result); err != nil {
			return nil, errors.New("local text: corrupt result")
		}
		if err := json.Unmarshal([]byte(evidence), &row.Snapshot.Evidence); err != nil {
			return nil, errors.New("local text: corrupt snapshot")
		}
		times, err := knowledgeTimes(sql.NullString{String: acquired, Valid: true}, sql.NullString{String: snapshotClock, Valid: true}, sql.NullString{String: captured, Valid: true})
		if err != nil {
			return nil, err
		}
		row.Snapshot.AcquiredAt, row.Snapshot.CapturedAt, row.CapturedAt = *times[0], *times[1], *times[2]
		row.Capture.Evidence, row.Capture.SourceByteDigest, row.Capture.AcquiredAt = row.Snapshot.Evidence, row.Snapshot.SourceByteDigest, row.Snapshot.AcquiredAt
		if err := validateLocalTextRow(ctx, row); err != nil {
			return nil, err
		}
		expectedModified := ""
		if row.Capture.Evidence.ModifiedAt != nil {
			expectedModified = row.Capture.Evidence.ModifiedAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
		canonicalEvidence, _ := json.Marshal(row.Snapshot.Evidence)
		canonicalResult, _ := json.Marshal(row.Capture.Result)
		if string(canonicalEvidence) != evidence || string(canonicalResult) != result || modified.String != expectedModified ||
			modified.Valid != (row.Capture.Evidence.ModifiedAt != nil) || captureSource != row.SourceID || captureLocator != locator ||
			captureDigest != row.RepresentationDigest || !member || (origin != "explicit" && origin != "extension") ||
			nativeSource != row.SourceID || nativeLocator != locator || nativeFinished != snapshotClock || !nativeMember ||
			qual != LocalTextQualification || !validLocalDigest(row.RepresentationDigest) {
			return nil, errors.New("local text: corrupt observation binding")
		}
		out = append(out, row)
		bindings = append(bindings, binding{locator, boundary, qual})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, row := range out {
		b := bindings[i]
		input, platform, err := readLocalRequest(ctx, r, b.locator)
		if err != nil {
			return nil, err
		}
		if err := officecapture.ValidatePlatformEvidence(platform, row.Capture.Evidence); err != nil {
			return nil, err
		}
		if row.Capture.RequestedKind != input.RequestedKind || localBoundary(input, platform, row.Capture.Evidence, row.CaptureID) != b.boundary ||
			localRepresentation(input, platform, b.boundary, row.Capture) != row.RepresentationDigest {
			return nil, errors.New("local text: corrupt representation or incarnation")
		}
		if err := validateLocalFTSRow(ctx, r, row.ID, localTextLabel(input, platform), LocalTextValues(row.Capture.Result), input.Path); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readLocalReferences(ctx context.Context, r knowledgeReader, observations []LocalTextRow) ([]LocalTextReferenceOccurrence, error) {
	rows, err := r.QueryContext(ctx, `select r.attempt_id,r.capture_id,r.observation_rowid,r.document_observation_id,r.original_url,r.association_state,
a.source_id,a.content_digest,a.finished_at,m.request_origin,
exists(select 1 from local_text_capture_members member where member.capture_id=r.capture_id and member.observation_rowid=r.observation_rowid)
from local_text_references r left join knowledge_attempts a on a.id=r.attempt_id
left join local_text_attempt_metadata m on m.attempt_id=r.attempt_id order by r.attempt_id,r.document_observation_id,r.original_url`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []LocalTextReferenceOccurrence{}
	for rows.Next() {
		var ref LocalTextReferenceOccurrence
		var source int64
		var digest, finished, origin string
		var member bool
		if err := rows.Scan(&ref.AttemptID, &ref.CaptureID, &ref.ObservationID, &ref.Reference.DocumentObservationID, &ref.Reference.OriginalURL, &ref.Reference.AssociationState,
			&source, &digest, &finished, &origin, &member); err != nil {
			return nil, err
		}
		index := slices.IndexFunc(observations, func(row LocalTextRow) bool { return row.ID == ref.ObservationID })
		if index < 0 || ref.AttemptID != ref.CaptureID || source != observations[index].SourceID || digest != observations[index].RepresentationDigest ||
			finished == "" || !member || (origin != "explicit" && origin != "extension") {
			return nil, errors.New("local text: corrupt reference binding")
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, ref := range out {
		if err := validateLocalReference(ctx, r, ref.Reference); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func validateLocalMemberships(ctx context.Context, r knowledgeReader) error {
	var corrupt int
	err := r.QueryRowContext(ctx, `select count(*) from local_text_capture_members m
left join local_text_observations o on o.rowid=m.observation_rowid
left join knowledge_attempts a on a.id=m.capture_id
left join knowledge_sources s on s.id=o.source_id
left join knowledge_locators l on l.id=s.locator_id
left join local_text_attempt_metadata am on am.attempt_id=m.capture_id
where o.rowid is null or a.id is null or s.id is null or l.kind<>?
or a.source_id is not o.source_id or a.locator_id is not s.locator_id
or a.content_digest is not o.representation_digest or a.finished_at is null
or a.outcome not in ('complete','partial') or am.request_origin not in ('explicit','extension') or am.attempt_id is null
or (select count(*) from local_text_capture_members mm where mm.capture_id=m.capture_id)<>1`, LocalTextKind).Scan(&corrupt)
	if err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("local text: corrupt capture memberships")
	}
	err = r.QueryRowContext(ctx, `select count(*) from knowledge_attempts a
join knowledge_locators l on l.id=a.locator_id
where l.kind=? and a.source_id is not null and
((select count(*) from local_text_capture_members m where m.capture_id=a.id)<>1
or (select count(*) from local_text_attempt_metadata am where am.attempt_id=a.id)<>1)`, LocalTextKind).Scan(&corrupt)
	if err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("local text: incomplete successful attempt")
	}
	err = r.QueryRowContext(ctx, `select count(*) from knowledge_locators l where l.kind=? and
(l.latest_attempt is not (select max(a.id) from knowledge_attempts a where a.locator_id=l.id)
or coalesce(l.last_admitted,0)<>(select coalesce(max(a.id),0) from knowledge_attempts a where a.locator_id=l.id and a.source_id is not null))
`, LocalTextKind).Scan(&corrupt)
	if err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("local text: corrupt registration frontier")
	}
	err = r.QueryRowContext(ctx, `select count(*) from knowledge_sources s join knowledge_locators l on l.id=s.locator_id
where l.kind=? and s.admitted_attempt is not (select max(a.id) from knowledge_attempts a where a.source_id=s.id)`, LocalTextKind).Scan(&corrupt)
	if err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("local text: corrupt incarnation frontier")
	}
	err = r.QueryRowContext(ctx, `select count(*) from knowledge_sources s join knowledge_locators l on l.id=s.locator_id
where l.kind=? and s.complete_attempt is not
(select max(a.id) from knowledge_attempts a where a.source_id=s.id and a.outcome='complete')`, LocalTextKind).Scan(&corrupt)
	if err != nil {
		return err
	}
	if corrupt != 0 {
		return errors.New("local text: corrupt complete frontier")
	}
	return nil
}

func validateLocalAttemptOutcomes(ctx context.Context, r knowledgeReader, observations []LocalTextRow) error {
	byID := map[int64]LocalTextRow{}
	for _, row := range observations {
		byID[row.ID] = row
	}
	rows, err := r.QueryContext(ctx, `select m.observation_rowid,a.outcome,a.coverage,a.losses_json,a.reason
from local_text_capture_members m join knowledge_attempts a on a.id=m.capture_id order by a.id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var status, coverage, encoded, reason string
		if err := rows.Scan(&id, &status, &coverage, &encoded, &reason); err != nil {
			return err
		}
		wantStatus, wantCoverage, wantLosses := localTextOutcome(byID[id].Capture.Result)
		var losses map[string]int
		if err := json.Unmarshal([]byte(encoded), &losses); err != nil {
			return errors.New("local text: corrupt attempt losses")
		}
		canonical, _ := json.Marshal(wantLosses)
		if status != wantStatus || coverage != wantCoverage || !maps.Equal(losses, wantLosses) || encoded != string(canonical) || reason != "" {
			return errors.New("local text: corrupt successful attempt outcome")
		}
	}
	return rows.Err()
}

func validateLocalFTSOrphans(ctx context.Context, r knowledgeReader) error {
	var count int
	if err := r.QueryRowContext(ctx, `select count(*) from local_text_fts f left join local_text_observations o on o.rowid=f.rowid where o.rowid is null`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("local text: orphan typed index")
	}
	return nil
}

func validateLocalFTSRow(ctx context.Context, r knowledgeReader, id int64, label, text, metadata string) error {
	var count, matching int
	if err := r.QueryRowContext(ctx, `select count(*),coalesce(sum(label is ? and text is ? and metadata is ?),0)
from local_text_fts where rowid=?`, label, text, metadata, id).Scan(&count, &matching); err != nil {
		return err
	}
	if count != 1 || matching != 1 {
		return errors.New("local text: corrupt typed index binding")
	}
	return nil
}

// readLocalState validates complete current and retained bindings before applying any hit/limit filters.
func readLocalState(ctx context.Context, r knowledgeReader) (LocalTextSnapshot, []LocalTextReferenceOccurrence, error) {
	out := LocalTextSnapshot{Rows: []LocalTextRow{}, Sources: []LocalTextSource{}}
	ok, err := hasLocalTextSchema(ctx, r)
	if err != nil || !ok {
		return out, nil, err
	}
	if err := validateLocalMemberships(ctx, r); err != nil {
		return out, nil, err
	}
	if err := validateLocalFTSOrphans(ctx, r); err != nil {
		return out, nil, err
	}
	out.Rows, err = readLocalRows(ctx, r)
	if err != nil {
		return out, nil, err
	}
	if err := validateLocalAttemptOutcomes(ctx, r, out.Rows); err != nil {
		return out, nil, err
	}
	sources, err := readKnowledgeSources(ctx, r, LocalTextKind)
	if err != nil {
		return out, nil, err
	}
	for _, source := range sources {
		input, _, err := readLocalRequest(ctx, r, source.LocatorID)
		if err != nil {
			return out, nil, err
		}
		item := LocalTextSource{Source: source, RegistrationID: source.LocatorID, Path: input.Path, RequestedKind: input.RequestedKind, SnapshotMatch: "unavailable"}
		if source.AttemptID <= 0 {
			return out, nil, errors.New("local text: missing registration attempt")
		}
		if source.ID > 0 {
			var current int64
			var good sql.NullInt64
			if err := r.QueryRowContext(ctx, `select current_snapshot_id,last_good_observation_id from local_text_source_state where source_id=?`, source.ID).Scan(&current, &good); err != nil {
				return out, nil, err
			}
			var admittedID int64
			var digest string
			if err := r.QueryRowContext(ctx, `select o.rowid,a.content_digest from local_text_capture_members m
join local_text_observations o on o.rowid=m.observation_rowid join knowledge_attempts a on a.id=m.capture_id
where m.capture_id=? and a.source_id=? and o.source_id=? and a.locator_id=?`, source.AdmittedID, source.ID, source.ID, source.LocatorID).Scan(&admittedID, &digest); err != nil {
				return out, nil, err
			}
			index := slices.IndexFunc(out.Rows, func(row LocalTextRow) bool { return row.ID == admittedID && row.SourceID == source.ID })
			if index < 0 || out.Rows[index].Snapshot.ID != current || out.Rows[index].RepresentationDigest != digest ||
				source.ContentDigest != digest || source.Qualification != LocalTextQualification {
				return out, nil, errors.New("local text: corrupt current snapshot")
			}
			snapshot := out.Rows[index].Snapshot
			item.CurrentSnapshot = &snapshot
			body := out.Rows[index].Capture.Result.State == "text_observations"
			if body && (!good.Valid || good.Int64 != admittedID) {
				return out, nil, errors.New("local text: invalid readable frontier")
			}
			if good.Valid {
				bodyIndex := slices.IndexFunc(out.Rows, func(row LocalTextRow) bool {
					return row.ID == good.Int64 && row.SourceID == source.ID && row.Capture.Result.State == "text_observations"
				})
				if bodyIndex < 0 {
					return out, nil, errors.New("local text: corrupt retained body")
				}
				value := good.Int64
				item.LastGoodObservationID = &value
				if snapshot.Evidence.GenerationQualified && out.Rows[bodyIndex].Snapshot.Evidence.GenerationQualified {
					item.SnapshotMatch = "differs"
					if out.Rows[bodyIndex].Snapshot.ID == snapshot.ID {
						item.SnapshotMatch = "matches"
					}
				}
			}
			// Original memberships on a superseded source do not make it current.
			for i := range out.Rows {
				if out.Rows[i].SourceID == source.ID {
					out.Rows[i].Retained = source.Status == "retained" || out.Rows[i].ID != admittedID
				}
			}
		}
		out.Sources = append(out.Sources, item)
	}
	refs, err := readLocalReferences(ctx, r, out.Rows)
	return out, refs, err
}

func (s *Store) LocalTextSources(ctx context.Context, id int64) ([]LocalTextSource, error) {
	snapshot, err := s.LocalTextSnapshot(ctx, LocalTextFilter{SourceID: id})
	return snapshot.Sources, err
}

func (s *Store) LocalTextSnapshot(ctx context.Context, f LocalTextFilter) (LocalTextSnapshot, error) {
	if f.SourceID < 0 || f.Limit < 0 || f.Limit == math.MaxInt {
		return LocalTextSnapshot{}, errors.New("local text: invalid filter")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LocalTextSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, _, err := readLocalState(ctx, tx)
	if err != nil {
		return out, err
	}
	if f.SourceID > 0 {
		out.Sources = slices.DeleteFunc(out.Sources, func(s LocalTextSource) bool { return s.Source.ID != f.SourceID })
		if len(out.Sources) == 0 {
			return out, ErrKnowledgeSourceUnknown
		}
	}
	var matches map[int64]bool
	if f.Query != "" && len(out.Sources) > 0 {
		match := buildFTSQuery(f.Query)
		if match == "" {
			return out, errs.Usage("content search requires searchable words")
		}
		rows, err := tx.QueryContext(ctx, `select rowid from local_text_fts where local_text_fts match ?`, match)
		if err != nil {
			return out, err
		}
		matches = map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return out, err
			}
			matches[id] = true
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return out, err
		}
	}
	out.Rows = slices.DeleteFunc(out.Rows, func(row LocalTextRow) bool {
		at := row.Snapshot.Evidence.ModifiedAt
		return (f.SourceID > 0 && row.SourceID != f.SourceID) || (f.Query != "" && !matches[row.ID]) ||
			(!f.ModifiedSince.IsZero() && (at == nil || at.Before(f.ModifiedSince))) ||
			(!f.ModifiedUntil.IsZero() && (at == nil || !at.Before(f.ModifiedUntil)))
	})
	labels := map[int64]string{}
	for _, src := range out.Sources {
		labels[src.Source.ID] = src.Path
	}
	slices.SortStableFunc(out.Rows, func(a, b LocalTextRow) int {
		aa, bb := a.Snapshot.Evidence.ModifiedAt, b.Snapshot.Evidence.ModifiedAt
		if aa != nil && bb == nil {
			return -1
		}
		if aa == nil && bb != nil {
			return 1
		}
		if aa != nil && bb != nil {
			if c := bb.Compare(*aa); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(labels[a.SourceID], labels[b.SourceID]); c != 0 {
			return c
		}
		if c := cmp.Compare(a.SourceID, b.SourceID); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if f.Limit > 0 && len(out.Rows) > f.Limit {
		out.Truncated = true
		out.Rows = out.Rows[:f.Limit]
	}
	return out, tx.Commit()
}

func (s *Store) LocalTextDetail(ctx context.Context, id int64) (LocalTextDetailResult, error) {
	if id <= 0 {
		return LocalTextDetailResult{}, ErrKnowledgeSourceUnknown
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LocalTextDetailResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, refs, err := readLocalState(ctx, tx)
	if err != nil {
		return LocalTextDetailResult{}, err
	}
	index := slices.IndexFunc(state.Rows, func(row LocalTextRow) bool { return row.ID == id })
	if index < 0 {
		return LocalTextDetailResult{}, ErrKnowledgeSourceUnknown
	}
	out := LocalTextDetailResult{Observation: state.Rows[index], References: []LocalTextReferenceOccurrence{}}
	for _, src := range state.Sources {
		if src.Source.ID == out.Observation.SourceID {
			out.Source = src
		}
	}
	for _, ref := range refs {
		if ref.ObservationID == id {
			out.ReferencesTotal++
			if len(out.References) < 100 {
				out.References = append(out.References, ref)
			} else {
				out.ReferencesTruncated = true
			}
		}
	}
	return out, tx.Commit()
}
