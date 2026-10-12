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
	currentDigest string
}

// LoopSources reads every registered Loop source with its current raw comparison in one read
// transaction, in registration ID order.
func (s *Store) LoopSources(ctx context.Context) ([]LoopSource, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	states, err := readLoopSources(ctx, tx, 0)
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
coalesce(sn.id,0),coalesce(sn.value_digest,''),coalesce(sn.qualification,''),coalesce(sn.tenant_id,''),coalesce(sn.user_id,'')
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
			&state.source.LastSnapshotTenantID, &state.source.LastSnapshotUserID); err != nil {
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

// compareLoopRaw acquires the source's current raw record and sets presence, owner evidence and
// the explicit snapshot match. Removal never changes a digest match; clocks never establish one.
func compareLoopRaw(ctx context.Context, reader knowledgeReader, state *loopSourceState) error {
	source := &state.source
	acquisition, err := readLoopTuple(ctx, reader, source.Registration)
	if err != nil {
		return err
	}
	source.Reason = acquisition.RefusalCode
	if acquisition.RecordID > 0 {
		var updated string
		var removed sql.NullString
		var tenant, user sql.NullString
		if err := reader.QueryRowContext(ctx, `select updated_at,removed_at,
case when length(cast(tenant_id as blob))<=? then tenant_id end,case when length(cast(user_id as blob))<=? then user_id end
from records where rowid=?`, LoopOwnerLimit, LoopOwnerLimit, acquisition.RecordID).Scan(&updated, &removed, &tenant, &user); err != nil {
			return err
		}
		source.RawUpdatedAt, source.RawRemovedAt = loopClock(updated), loopClock(removed.String)
		source.RawPresent = !removed.Valid
		source.Registration.TenantID, source.Registration.UserID = tenant.String, user.String
	}
	if acquisition.Frame != nil {
		source.RawValuePresent = true
		state.currentDigest = acquisition.Frame.ValueDigest
	}
	switch {
	case source.LastSnapshotID == 0:
		source.SnapshotMatch = "no_admission"
	case acquisition.Frame == nil:
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
	snapshot, err := readLoopSnapshot(ctx, tx, filter, 0)
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
coalesce(e.snapshot_id,0)<>sn.id
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
			&row.SnapshotTenantID, &row.SnapshotUserID, &observed, &projected, &row.valueDigest, &row.qualification, &row.Retained); err != nil {
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
	rows, _, err := readLoopChannels(ctx, tx, LoopFilter{}, id)
	if err != nil {
		return LoopChannelRow{}, LoopSource{}, err
	}
	if len(rows) == 0 {
		return LoopChannelRow{}, LoopSource{}, ErrLoopChannelNotFound
	}
	snapshot, err := readLoopSnapshot(ctx, tx, LoopFilter{SourceID: rows[0].RegistrationID}, id)
	if err != nil {
		return LoopChannelRow{}, LoopSource{}, err
	}
	return snapshot.Rows[0], snapshot.Sources[0], tx.Commit()
}
