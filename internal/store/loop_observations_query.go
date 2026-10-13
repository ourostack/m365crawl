package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/loopcache"
)

// loopSourceState is a registered source with the comparison inputs of its last admitted snapshot.
type loopSourceState struct {
	source        LoopSource
	digest, qual  string
	admitted      loopFingerprint // the record fingerprint stored by the last admitted attempt
	currentDigest string
}

// loopReader is the reader a public read hands to its queries; tests wrap it to publish between them.
var loopReader = func(tx *sql.Tx) knowledgeReader { return tx }

// LoopSources reads one registered Loop source, or all for ID zero, with its current raw comparison
// in one read transaction, in registration ID order. An unknown positive ID is
// ErrKnowledgeSourceUnknown.
func (s *Store) LoopSources(ctx context.Context, registrationID int64) ([]LoopSource, error) {
	if registrationID < 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	states, err := readLoopSources(ctx, loopReader(tx), registrationID)
	if err != nil {
		return nil, err
	}
	out := make([]LoopSource, len(states))
	for i, state := range states {
		out[i] = state.source
	}
	return out, tx.Commit()
}

// readLoopSources reads registrations (one, or all for zero), their current knowledge health, last
// admitted snapshot and current raw frame, and compares them. An unknown positive ID is
// ErrKnowledgeSourceUnknown.
func readLoopSources(ctx context.Context, reader knowledgeReader, registrationID int64) ([]loopSourceState, error) {
	available, err := hasLoopSchema(ctx, reader)
	if err != nil {
		return nil, err
	}
	if !available {
		if registrationID != 0 {
			return nil, ErrKnowledgeSourceUnknown
		}
		return []loopSourceState{}, nil
	}
	health, err := readKnowledgeSources(ctx, reader, LoopKind)
	if err != nil {
		return nil, err
	}
	current := map[int64]KnowledgeSource{}
	for _, source := range health {
		if source.Status != "retained" {
			current[source.LocatorID] = source
		}
	}
	rows, err := reader.QueryContext(ctx, `select m.locator_id,m.source,m.database,m.store,m.key_json,
coalesce(sn.id,0),coalesce(sn.value_digest,''),coalesce(sn.qualification,''),coalesce(sn.tenant_id,''),coalesce(sn.user_id,''),
coalesce(e.record_content_hash,''),coalesce(e.record_value_length,0),coalesce(e.record_updated_at,''),e.record_raw_digest,
coalesce(e.record_value_redacted,0)
from loop_locator_metadata m join knowledge_locators l on l.id=m.locator_id
left join loop_attempt_evidence e on e.attempt_id=l.last_admitted
left join loop_snapshots sn on sn.id=e.snapshot_id
where l.kind=? and l.platform=? and (?=0 or m.locator_id=?) order by m.locator_id`, LoopKind, LoopPlatform, registrationID, registrationID)
	if err != nil {
		return nil, err
	}
	states := []loopSourceState{}
	for rows.Next() {
		var state loopSourceState
		registration := &state.source.Registration
		if err := rows.Scan(&registration.ID, &registration.Tuple.Source, &registration.Tuple.Database, &registration.Tuple.Store,
			&registration.Tuple.KeyJSON, &state.source.LastSnapshotID, &state.digest, &state.qual,
			&state.source.LastSnapshotTenantID, &state.source.LastSnapshotUserID, &state.admitted.contentHash,
			&state.admitted.valueLength, &state.admitted.updated, &state.admitted.rawDigest, &state.admitted.redacted); err != nil {
			_ = rows.Close()
			return nil, err
		}
		state.source.Health = current[registration.ID]
		state.source.SnapshotQualification = state.qual
		states = append(states, state)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if registrationID != 0 && len(states) == 0 {
		return nil, ErrKnowledgeSourceUnknown
	}
	for i := range states {
		if err := compareLoopRaw(ctx, reader, &states[i]); err != nil {
			return nil, err
		}
	}
	return states, nil
}

// compareLoopRaw reads the source's current raw record without its value and sets presence,
// validated owner evidence and the explicit snapshot match. The value is loaded and hashed only
// when the record's fingerprint differs from the one its last admission stored. Removal never
// changes a match; clocks alone never establish one.
func compareLoopRaw(ctx context.Context, reader knowledgeReader, state *loopSourceState) error {
	source := &state.source
	tuple := source.Registration.Tuple
	var recordID int64
	err := reader.QueryRowContext(ctx, `select rowid from records where source=? and database=? and store=? and key_json=?`,
		tuple.Source, tuple.Database, tuple.Store, tuple.KeyJSON).Scan(&recordID)
	acquirable := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		source.Reason = "loop_cache_record_missing"
	case err != nil:
		return err
	default:
		identity, record, err := readLoopIdentity(ctx, reader, recordID)
		if err != nil {
			return err
		}
		source.Reason = identity.RefusalCode
		source.RawUpdatedAt, source.RawRemovedAt = loopClock(record.fingerprint.updated), loopClock(record.removed.String)
		source.RawPresent = !record.removed.Valid
		if identity.Registration != nil { // owner strings leave the archive only after validation
			source.Registration.TenantID, source.Registration.UserID = identity.Registration.TenantID, identity.Registration.UserID
		}
		if acquirable = identity.RefusalCode == ""; acquirable {
			source.RawValuePresent = true
			state.currentDigest = state.digest
			// Without an admitted snapshot there is nothing to compare, so nothing is loaded.
			if source.LastSnapshotID != 0 && !state.admitted.matches(record.fingerprint) {
				value, err := loopReadValue(ctx, reader, recordID)
				if err != nil {
					return err
				}
				state.currentDigest = loopDigest(value)
			}
		}
	}
	switch {
	case source.LastSnapshotID == 0:
		source.SnapshotMatch = "no_admission"
	case !acquirable:
		source.SnapshotMatch = "unavailable"
	case state.currentDigest == state.digest && state.qual == LoopQualification:
		source.SnapshotMatch = "matches"
	default:
		source.SnapshotMatch = "differs"
	}
	return nil
}

// loopClock is a stored raw clock, or nil when absent or unparsable (it stays unknown).
func loopClock(value string) *time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &at
}

// LoopSnapshot reads the filtered channel rows and the participating source coverage, including
// each row's raw comparison, in one read transaction.
func (s *Store) LoopSnapshot(ctx context.Context, filter LoopFilter) (LoopQuerySnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LoopQuerySnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := readLoopSnapshot(ctx, loopReader(tx), filter, 0)
	if err != nil {
		return LoopQuerySnapshot{}, err
	}
	return snapshot, tx.Commit()
}

func readLoopSnapshot(ctx context.Context, reader knowledgeReader, filter LoopFilter, channelID int64) (LoopQuerySnapshot, error) {
	if filter.Limit < 0 || filter.Limit == math.MaxInt || filter.SourceID < 0 {
		return LoopQuerySnapshot{}, errors.New("loop: invalid filter")
	}
	rows, truncated, err := readLoopChannels(ctx, reader, filter, channelID)
	if err != nil {
		return LoopQuerySnapshot{}, err
	}
	states, err := readLoopSources(ctx, reader, filter.SourceID)
	if err != nil {
		return LoopQuerySnapshot{}, err
	}
	byID := map[int64]loopSourceState{}
	out := LoopQuerySnapshot{Rows: rows, Sources: make([]LoopSource, len(states)), Truncated: truncated}
	if len(rows) > 0 {
		out.Matched = rows[0].matched // the same count on every row: the window spans the whole match
	}
	for i, state := range states {
		byID[state.source.Registration.ID] = state
		out.Sources[i] = state.source
	}
	for i := range out.Rows {
		row := &out.Rows[i]
		state := byID[row.RegistrationID]
		row.RawPresent, row.RawUpdatedAt, row.RawRemovedAt = state.source.RawPresent, state.source.RawUpdatedAt, state.source.RawRemovedAt
		row.MatchesRetainedValue = state.currentDigest != "" && state.currentDigest == row.valueDigest && row.qualification == LoopQualification
	}
	return out, nil
}

func readLoopChannels(ctx context.Context, reader knowledgeReader, filter LoopFilter, channelID int64) ([]LoopChannelRow, bool, error) {
	available, err := hasLoopSchema(ctx, reader)
	if err != nil || !available {
		return []LoopChannelRow{}, false, err
	}
	where := "l.kind=? and l.platform=?"
	args := []any{LoopKind, LoopPlatform}
	if channelID > 0 {
		where += " and c.id=?"
		args = append(args, channelID)
	}
	if filter.SourceID > 0 {
		where += " and sn.locator_id=?"
		args = append(args, filter.SourceID)
	}
	if filter.Query != "" {
		match := buildFTSQuery(filter.Query)
		if match == "" {
			return nil, false, errs.Usage("loop search requires searchable words")
		}
		where += " and c.id in (select rowid from loop_fts where loop_fts match ?)"
		args = append(args, match)
	}
	limit := ""
	if filter.Limit > 0 {
		limit = " limit ?"
		args = append(args, filter.Limit+1)
	}
	rows, err := reader.QueryContext(ctx, `select c.id,sn.locator_id,sn.id,c.path,c.sequence,c.text,c.markers,c.pending_operations,
sn.file_id,sn.sequence,sn.latest_sequence,sn.pending_operations,sn.missing_blobs,sn.partial,sn.tenant_id,sn.user_id,
sn.record_updated_at_at_admission,sn.projected_at,sn.value_digest,sn.qualification,
coalesce(e.snapshot_id,0)<>sn.id,count(*) over()
from loop_channels c join loop_snapshots sn on sn.id=c.snapshot_id
join knowledge_locators l on l.id=sn.locator_id
left join loop_attempt_evidence e on e.attempt_id=l.last_admitted
where `+where+` order by c.path collate nocase,sn.locator_id,sn.id,c.id`+limit, args...)
	if err != nil {
		return nil, false, err
	}
	out := []LoopChannelRow{}
	for rows.Next() {
		var row LoopChannelRow
		var observed, projected string
		if err := rows.Scan(&row.ID, &row.RegistrationID, &row.SnapshotID, &row.Channel.Path, &row.Channel.Sequence,
			&row.Channel.Text, &row.Channel.Markers, &row.Channel.PendingOperations, &row.Snapshot.FileID, &row.Snapshot.Sequence,
			&row.Snapshot.LatestSequence, &row.Snapshot.PendingOperations, &row.Snapshot.MissingBlobs, &row.Snapshot.Partial,
			&row.SnapshotTenantID, &row.SnapshotUserID, &observed, &projected, &row.valueDigest, &row.qualification, &row.Retained, &row.matched); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		times, err := knowledgeTimes(sql.NullString{String: observed, Valid: true}, sql.NullString{String: projected, Valid: true})
		if err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		row.RecordUpdatedAtAtAdmission, row.ProjectedAt = *times[0], *times[1]
		out = append(out, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	truncated := filter.Limit > 0 && len(out) > filter.Limit
	if truncated {
		out = out[:filter.Limit]
	}
	for i := range out {
		losses, err := readLoopLosses(ctx, reader, out[i].SnapshotID)
		if err != nil {
			return nil, false, err
		}
		out[i].Snapshot.Losses = losses
	}
	return out, truncated, nil
}

func readLoopLosses(ctx context.Context, reader knowledgeReader, snapshotID int64) ([]loopcache.Loss, error) {
	rows, err := reader.QueryContext(ctx, `select code,count from loop_snapshot_losses where snapshot_id=? order by code`, snapshotID)
	if err != nil {
		return nil, err
	}
	losses := []loopcache.Loss{}
	for rows.Next() {
		var loss loopcache.Loss
		if err := rows.Scan(&loss.Code, &loss.Count); err != nil {
			_ = rows.Close()
			return nil, err
		}
		losses = append(losses, loss)
	}
	return losses, errors.Join(rows.Err(), rows.Close())
}

// LoopChannel reads one immutable channel observation and its original registration's coverage in
// one read transaction.
func (s *Store) LoopChannel(ctx context.Context, id int64) (LoopChannelRow, LoopSource, error) {
	if id <= 0 {
		return LoopChannelRow{}, LoopSource{}, ErrLoopChannelNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LoopChannelRow{}, LoopSource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	reader := loopReader(tx)
	rows, _, err := readLoopChannels(ctx, reader, LoopFilter{}, id)
	if err != nil {
		return LoopChannelRow{}, LoopSource{}, err
	}
	if len(rows) == 0 {
		return LoopChannelRow{}, LoopSource{}, ErrLoopChannelNotFound
	}
	snapshot, err := readLoopSnapshot(ctx, reader, LoopFilter{SourceID: rows[0].RegistrationID}, id)
	if err != nil {
		return LoopChannelRow{}, LoopSource{}, err
	}
	return snapshot.Rows[0], snapshot.Sources[0], tx.Commit()
}

// LoopOverview is what the archive holds of cached Loop channels, from counts only: no value is
// loaded or hashed.
type LoopOverview struct {
	ArchivedRecords, Registered, Failed, MetadataOnly, Channels int
	LastProjectedAt                                             *time.Time
}

// LoopOverview counts archived Loop records, registered sources by their latest attempt, and
// channel observations. An archive older than the Loop tables reports only the archived records.
func (s *Store) LoopOverview(ctx context.Context) (LoopOverview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LoopOverview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var out LoopOverview
	if err := tx.QueryRowContext(ctx, `select count(*) from records where database=? and store=?`, LoopDatabase, LoopStore).
		Scan(&out.ArchivedRecords); err != nil {
		return LoopOverview{}, err
	}
	available, err := hasLoopSchema(ctx, tx)
	if err != nil || !available {
		return out, err
	}
	health, err := readKnowledgeSources(ctx, tx, LoopKind)
	if err != nil {
		return LoopOverview{}, err
	}
	for _, source := range health {
		if source.Status == "retained" {
			continue
		}
		out.Registered++
		switch {
		case source.Status == "failed" || source.Status == "cancelled":
			out.Failed++
		case source.Coverage == "metadata_only":
			out.MetadataOnly++
		}
		if source.AdmittedAt != nil && (out.LastProjectedAt == nil || source.AdmittedAt.After(*out.LastProjectedAt)) {
			out.LastProjectedAt = source.AdmittedAt
		}
	}
	if err := tx.QueryRowContext(ctx, `select count(*) from loop_channels`).Scan(&out.Channels); err != nil {
		return LoopOverview{}, err
	}
	return out, tx.Commit()
}
