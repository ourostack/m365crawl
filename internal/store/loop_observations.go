package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/loopcache"
)

// The exact archived tuple domain of cached Loop snapshots, and the knowledge kind/platform that
// registers each tuple as a Loop source.
const (
	LoopDatabase = "fluidDriverCache"
	LoopStore    = "driverStorage.V3"
	LoopKind     = "loop_cache"
	LoopPlatform = "archive"
)

// Inclusive UTF-8 byte bounds of the Loop consumer policy, checked by SQL length guards before any
// variable identity, owner or value field is scanned. They are finite consumer limits, not
// observed native maxima.
const (
	LoopValueLimit     = 8 << 20
	LoopSourceLimit    = 4 << 10
	LoopNameLimit      = 64
	LoopKeyLimit       = 256 << 10
	LoopOwnerLimit     = 4 << 10
	LoopDiscoveryBatch = 128
)

// loopLocatorLimit bounds the encoded four-string locator (2 MiB). The other bounds keep a real
// tuple far below it; tests lower it to prove the check.
var loopLocatorLimit = 2 << 20

// LoopQualification names the reader/mapper whose output a snapshot holds. A change makes every
// registered source's admitted snapshot differ, so the next refresh projects it again. It is a
// variable only so tests can model a parser change.
var LoopQualification = "loop-cache-v1/mapper-v1"

var (
	// ErrLoopInputChanged is a publication refused because the archived record changed in any
	// dimension after its frame was acquired; nothing was written.
	ErrLoopInputChanged = errors.New("loop: archived input changed during projection")
	// ErrLoopDiscoveryLimit is a discovery with more unregistered structural rows than one batch.
	ErrLoopDiscoveryLimit = errors.New("loop_cache_discovery_limit")
	// ErrLoopChannelNotFound is an unknown loop-channel ID.
	ErrLoopChannelNotFound = errors.New("loop: channel not found")
)

// LoopFailureReasons are the fixed reasons a Loop attempt can fail with.
var LoopFailureReasons = []string{"loop_cache_identity_unqualified", "loop_cache_record_missing", "loop_cache_value_missing",
	"loop_cache_value_too_large", "loop_cache_value_unqualified", "loop_cache_snapshot_unsupported",
	"loop_cache_snapshot_too_large", "loop_cache_reader_failed", "loop_cache_input_changed", "loop_cache_cancelled"}

type LoopTuple struct{ Source, Database, Store, KeyJSON string }

type LoopRawFrame struct {
	RecordID         int64
	Tuple            LoopTuple
	TenantID, UserID string
	Value            []byte
	ValuePresent     bool
	ValueDigest      string
	RawDigest        []byte
	ValueRedacted    bool
	UpdatedAt        time.Time
	RemovedAt        *time.Time
}

type LoopAcquisition struct {
	RecordID     int64
	Registration *LoopRegistration // nil only for an identity-invalid candidate
	Frame        *LoopRawFrame     // nonnil only after complete bounded value acquisition
	RefusalCode  string            // fixed source-local guard reason, never SQL/cancellation
}

type LoopDiscovery struct{ Candidates []LoopAcquisition }

type LoopRegistration struct {
	ID               int64 // knowledge locator ID, the public source_id
	Tuple            LoopTuple
	TenantID, UserID string // this acquisition's bounded owner evidence, not identity
}

type LoopSource struct {
	Registration                             LoopRegistration
	Health                                   KnowledgeSource
	LastSnapshotID                           int64
	SnapshotMatch                            string // no_admission, matches, differs, unavailable
	SnapshotQualification                    string
	LastSnapshotTenantID, LastSnapshotUserID string
	RawValuePresent, RawPresent              bool
	RawUpdatedAt, RawRemovedAt               *time.Time
	Reason                                   string // current raw refusal, empty when the value is acquirable
}

type LoopChannelRow struct {
	ID, RegistrationID, SnapshotID             int64
	Channel                                    loopcache.Channel
	Snapshot                                   loopcache.Result
	SnapshotTenantID, SnapshotUserID           string
	RecordUpdatedAtAtAdmission, ProjectedAt    time.Time
	MatchesRetainedValue, Retained, RawPresent bool
	RawUpdatedAt, RawRemovedAt                 *time.Time
	valueDigest, qualification                 string
}

type LoopFilter struct {
	Query    string
	SourceID int64
	Limit    int
}

type LoopQuerySnapshot struct {
	Rows      []LoopChannelRow
	Sources   []LoopSource
	Truncated bool
}

func hasLoopSchema(ctx context.Context, reader knowledgeReader) (bool, error) {
	var count int
	err := reader.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table'
and name in ('loop_locator_metadata','loop_attempt_evidence','loop_snapshots','loop_snapshot_losses','loop_channels','loop_fts')`).Scan(&count)
	return count == 6, err
}

func loopDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// loopLocator is the deterministic encoding of the ordered four-string tuple, or false when the
// tuple is outside the consumer policy.
func loopLocator(tuple LoopTuple) (string, bool) {
	if tuple.Database != LoopDatabase || tuple.Store != LoopStore || len(tuple.Source) > LoopSourceLimit ||
		len(tuple.KeyJSON) > LoopKeyLimit || !utf8.ValidString(tuple.Source) || !utf8.ValidString(tuple.KeyJSON) {
		return "", false
	}
	encoded, _ := json.Marshal([]string{tuple.Source, tuple.Database, tuple.Store, tuple.KeyJSON}) // strings always encode
	if len(encoded) > loopLocatorLimit {
		return "", false
	}
	return string(encoded), true
}

func validLoopOwner(tenant, user string) bool {
	return len(tenant) <= LoopOwnerLimit && len(user) <= LoopOwnerLimit && utf8.ValidString(tenant) && utf8.ValidString(user)
}

// loopReadValue loads one bounded value; tests wrap it to count allocations.
var loopReadValue = func(ctx context.Context, reader knowledgeReader, recordID int64) ([]byte, error) {
	var present bool
	var value []byte
	err := reader.QueryRowContext(ctx, `select value_json is not null,cast(value_json as blob) from records where rowid=?`, recordID).
		Scan(&present, &value)
	if value == nil {
		value = []byte{}
	}
	return value, err
}

// readLoopAcquisition reads one structurally matching record: SQL type/length guards first, then
// the bounded identity/owner strings, then the bounded value.
func readLoopAcquisition(ctx context.Context, reader knowledgeReader, recordID int64) (LoopAcquisition, error) {
	acquisition := LoopAcquisition{RecordID: recordID}
	var sourceType, keyType, tenantType, userType, valueType string
	var sourceLen, keyLen, tenantLen, userLen, valueLen int64
	var digest []byte
	var redacted int
	var updated string
	var removed sql.NullString
	if err := reader.QueryRowContext(ctx, `select typeof(source),typeof(key_json),typeof(tenant_id),typeof(user_id),
coalesce(length(cast(source as blob)),0),coalesce(length(cast(key_json as blob)),0),
coalesce(length(cast(tenant_id as blob)),0),coalesce(length(cast(user_id as blob)),0),
typeof(value_json),coalesce(length(cast(value_json as blob)),0),raw_digest,value_redacted,updated_at,removed_at
from records where rowid=? and database=? and store=?`, recordID, LoopDatabase, LoopStore).Scan(&sourceType, &keyType,
		&tenantType, &userType, &sourceLen, &keyLen, &tenantLen, &userLen, &valueType, &valueLen, &digest, &redacted,
		&updated, &removed); err != nil {
		return acquisition, err
	}
	if sourceType != "text" || keyType != "text" || tenantType != "text" || userType != "text" ||
		sourceLen > LoopSourceLimit || keyLen > LoopKeyLimit || tenantLen > LoopOwnerLimit || userLen > LoopOwnerLimit {
		acquisition.RefusalCode = "loop_cache_identity_unqualified"
		return acquisition, nil
	}
	registration := LoopRegistration{Tuple: LoopTuple{Database: LoopDatabase, Store: LoopStore}}
	if err := reader.QueryRowContext(ctx, `select source,key_json,tenant_id,user_id from records where rowid=?`, recordID).
		Scan(&registration.Tuple.Source, &registration.Tuple.KeyJSON, &registration.TenantID, &registration.UserID); err != nil {
		return acquisition, err
	}
	if _, ok := loopLocator(registration.Tuple); !ok || !validLoopOwner(registration.TenantID, registration.UserID) {
		acquisition.RefusalCode = "loop_cache_identity_unqualified"
		return acquisition, nil
	}
	acquisition.Registration = &registration
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	var removedAt *time.Time
	if removed.Valid {
		at, parseErr := time.Parse(time.RFC3339Nano, removed.String)
		removedAt, err = &at, errors.Join(err, parseErr)
	}
	switch {
	case valueType == "null":
		acquisition.RefusalCode = "loop_cache_value_missing"
	case valueType != "text" || err != nil:
		acquisition.RefusalCode = "loop_cache_value_unqualified"
	case valueLen > LoopValueLimit:
		acquisition.RefusalCode = "loop_cache_value_too_large"
	}
	if acquisition.RefusalCode != "" {
		return acquisition, nil
	}
	value, err := loopReadValue(ctx, reader, recordID)
	if err != nil {
		return acquisition, err
	}
	acquisition.Frame = &LoopRawFrame{RecordID: recordID, Tuple: registration.Tuple, TenantID: registration.TenantID,
		UserID: registration.UserID, Value: value, ValuePresent: true, ValueDigest: loopDigest(value), RawDigest: digest,
		ValueRedacted: redacted != 0, UpdatedAt: updatedAt, RemovedAt: removedAt}
	return acquisition, nil
}

// LoopDiscoveryFrames inspects at most one batch of unregistered records with the exact Loop
// database/store in record order, in one read transaction. More structural rows than a batch is
// ErrLoopDiscoveryLimit before any value is loaded.
func (s *Store) LoopDiscoveryFrames(ctx context.Context) (LoopDiscovery, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LoopDiscovery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `select r.rowid from records r where r.database=? and r.store=?
and not exists(select 1 from loop_locator_metadata m where m.source=r.source and m.database=r.database
and m.store=r.store and m.key_json=r.key_json) order by r.rowid limit ?`, LoopDatabase, LoopStore, LoopDiscoveryBatch+1)
	if err != nil {
		return LoopDiscovery{}, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return LoopDiscovery{}, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return LoopDiscovery{}, err
	}
	if len(ids) > LoopDiscoveryBatch {
		return LoopDiscovery{}, ErrLoopDiscoveryLimit
	}
	discovery := LoopDiscovery{Candidates: []LoopAcquisition{}}
	for _, id := range ids {
		acquisition, err := readLoopAcquisition(ctx, tx, id)
		if err != nil {
			return LoopDiscovery{}, err
		}
		discovery.Candidates = append(discovery.Candidates, acquisition)
	}
	return discovery, tx.Commit()
}

// readLoopRegistration reads the immutable tuple of a registered Loop locator.
func readLoopRegistration(ctx context.Context, reader knowledgeReader, id int64) (LoopRegistration, error) {
	registration := LoopRegistration{ID: id}
	err := reader.QueryRowContext(ctx, `select m.source,m.database,m.store,m.key_json from loop_locator_metadata m
join knowledge_locators l on l.id=m.locator_id where m.locator_id=? and l.kind=? and l.platform=?`, id, LoopKind, LoopPlatform).
		Scan(&registration.Tuple.Source, &registration.Tuple.Database, &registration.Tuple.Store, &registration.Tuple.KeyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return registration, ErrKnowledgeSourceUnknown
	}
	return registration, err
}

// readLoopTuple acquires the current record of a registered tuple. A vanished record is the fixed
// refusal loop_cache_record_missing, and an owner now outside the policy keeps the registration.
func readLoopTuple(ctx context.Context, reader knowledgeReader, registration LoopRegistration) (LoopAcquisition, error) {
	var recordID int64
	err := reader.QueryRowContext(ctx, `select rowid from records where source=? and database=? and store=? and key_json=?`,
		registration.Tuple.Source, registration.Tuple.Database, registration.Tuple.Store, registration.Tuple.KeyJSON).Scan(&recordID)
	if errors.Is(err, sql.ErrNoRows) {
		return LoopAcquisition{Registration: &registration, RefusalCode: "loop_cache_record_missing"}, nil
	}
	if err != nil {
		return LoopAcquisition{}, err
	}
	acquisition, err := readLoopAcquisition(ctx, reader, recordID)
	if err != nil {
		return LoopAcquisition{}, err
	}
	if acquisition.Registration == nil {
		acquisition.Registration = &LoopRegistration{Tuple: registration.Tuple}
	}
	acquisition.Registration.ID = registration.ID
	return acquisition, nil
}

// LoopRawFrame acquires the current frame of one registered source in one read transaction.
func (s *Store) LoopRawFrame(ctx context.Context, registrationID int64) (LoopAcquisition, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LoopAcquisition{}, err
	}
	defer func() { _ = tx.Rollback() }()
	available, err := hasLoopSchema(ctx, tx)
	if err != nil {
		return LoopAcquisition{}, err
	}
	if !available || registrationID <= 0 {
		return LoopAcquisition{}, ErrKnowledgeSourceUnknown
	}
	registration, err := readLoopRegistration(ctx, tx, registrationID)
	if err != nil {
		return LoopAcquisition{}, err
	}
	acquisition, err := readLoopTuple(ctx, tx, registration)
	if err != nil {
		return LoopAcquisition{}, err
	}
	return acquisition, tx.Commit()
}

// BeginLoopAttempt registers (ID zero) or selects (positive ID) the exact tuple and starts an
// attempt recording this acquisition's owner evidence. Owner evidence never selects or allocates a
// registration.
func (s *Store) BeginLoopAttempt(ctx context.Context, registration LoopRegistration, at time.Time) (KnowledgeAttempt, error) {
	locator, ok := loopLocator(registration.Tuple)
	if !ok || registration.ID < 0 || !validLoopOwner(registration.TenantID, registration.UserID) {
		return KnowledgeAttempt{}, errors.New("loop: invalid registration")
	}
	return s.beginKnowledgeAttempt(ctx, KnowledgeInput{Kind: LoopKind, Platform: LoopPlatform, Locator: locator, At: at},
		func(tx *sql.Tx, locatorID int64) error {
			if registration.ID > 0 && locatorID != registration.ID {
				return ErrKnowledgeSourceUnknown
			}
			tuple := registration.Tuple
			if _, err := tx.ExecContext(ctx, `insert into loop_locator_metadata(locator_id,source,database,store,key_json)
values(?,?,?,?,?) on conflict(locator_id) do nothing`, locatorID, tuple.Source, tuple.Database, tuple.Store, tuple.KeyJSON); err != nil {
				return err
			}
			stored, err := readLoopRegistration(ctx, tx, locatorID)
			if err != nil {
				return err
			}
			if stored.Tuple != tuple {
				return errors.New("loop: registration binding changed")
			}
			return nil
		},
		func(tx *sql.Tx, attempt KnowledgeAttempt) error {
			_, err := tx.ExecContext(ctx, `insert into loop_attempt_evidence(attempt_id,locator_id,tenant_id,user_id) values(?,?,?,?)`,
				attempt.ID, attempt.LocatorID, registration.TenantID, registration.UserID)
			return err
		})
}

// RecordLoopFailure finishes a still-current attempt with a fixed reason; a superseded attempt
// returns ErrStaleKnowledgeAttempt and changes nothing newer.
func (s *Store) RecordLoopFailure(ctx context.Context, attempt KnowledgeAttempt, reason string, at time.Time) error {
	if !slices.Contains(LoopFailureReasons, reason) {
		return errors.New("loop: invalid failure reason")
	}
	status := "failed"
	if reason == "loop_cache_cancelled" {
		status = "cancelled"
	}
	return s.finishKnowledge(ctx, attempt, KnowledgeOutcome{Status: status, Reason: reason, At: at}, LoopKind, nil)
}

func sameLoopFrame(a, b LoopRawFrame) bool {
	sameRemoval := (a.RemovedAt == nil) == (b.RemovedAt == nil) && (a.RemovedAt == nil || a.RemovedAt.Equal(*b.RemovedAt))
	return a.RecordID == b.RecordID && a.Tuple == b.Tuple && a.TenantID == b.TenantID && a.UserID == b.UserID &&
		a.ValuePresent == b.ValuePresent && a.ValueDigest == b.ValueDigest && bytes.Equal(a.RawDigest, b.RawDigest) &&
		a.ValueRedacted == b.ValueRedacted && a.UpdatedAt.Equal(b.UpdatedAt) && sameRemoval
}

// CommitLoopObservations publishes one parsed frame. Under the write transaction it re-reads the
// raw frame and the latest attempt; any changed dimension is ErrLoopInputChanged with nothing
// written. An identical digest/qualification reuses the existing immutable snapshot and its clocks.
func (s *Store) CommitLoopObservations(ctx context.Context, attempt KnowledgeAttempt, frame LoopRawFrame, result loopcache.Result, at time.Time) error {
	if !frame.ValuePresent || frame.RecordID <= 0 || frame.ValueDigest != loopDigest(frame.Value) || at.IsZero() {
		return errors.New("loop: unqualified frame")
	}
	for _, loss := range result.Losses {
		if loss.Code == "" || loss.Count <= 0 {
			return errors.New("loop: invalid reader loss")
		}
	}
	status, coverage, reason := "complete", "channels", ""
	if result.Partial || len(result.Losses) > 0 {
		status, coverage = "partial", "partial"
	}
	if len(result.Channels) == 0 {
		coverage, reason = "metadata_only", "loop_cache_metadata_only"
	}
	outcome := KnowledgeOutcome{Status: status, Reason: reason, ContentDigest: frame.ValueDigest, Qualification: LoopQualification, At: at}
	return s.finishKnowledge(ctx, attempt, outcome, LoopKind,
		func(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome) (int64, string, map[string]int, error) {
			return admitLoop(ctx, tx, attempt, outcome, frame, result, coverage)
		})
}

func admitLoop(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, outcome KnowledgeOutcome, frame LoopRawFrame, result loopcache.Result, coverage string) (int64, string, map[string]int, error) {
	registration, err := readLoopRegistration(ctx, tx, attempt.LocatorID)
	if err != nil {
		return 0, "", nil, err
	}
	if registration.Tuple != frame.Tuple {
		return 0, "", nil, errors.New("loop: frame belongs to another registration")
	}
	current, err := readLoopTuple(ctx, tx, registration)
	if err != nil {
		return 0, "", nil, err
	}
	if current.Frame == nil || !sameLoopFrame(frame, *current.Frame) {
		return 0, "", nil, ErrLoopInputChanged
	}
	if _, err := tx.ExecContext(ctx, `insert into knowledge_sources(locator_id,boundary,qualification)
values(?,'unresolved',?) on conflict(locator_id,boundary,qualification) do nothing`, attempt.LocatorID, outcome.Qualification); err != nil {
		return 0, "", nil, err
	}
	var sourceID, frontier int64
	if err := tx.QueryRowContext(ctx, `select id,coalesce(admitted_attempt,0) from knowledge_sources
where locator_id=? and boundary='unresolved' and qualification=?`, attempt.LocatorID, outcome.Qualification).Scan(&sourceID, &frontier); err != nil {
		return 0, "", nil, err
	}
	if frontier >= attempt.ID {
		return 0, "", nil, ErrStaleKnowledgeAttempt
	}
	var snapshotID int64
	err = tx.QueryRowContext(ctx, `select id from loop_snapshots where locator_id=? and value_digest=? and qualification=?`,
		attempt.LocatorID, frame.ValueDigest, outcome.Qualification).Scan(&snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		snapshotID, err = insertLoopSnapshot(ctx, tx, attempt, sourceID, frame, result, outcome)
	}
	if err != nil {
		return 0, "", nil, err
	}
	losses := map[string]int{}
	for _, loss := range result.Losses {
		losses[loss.Code] += loss.Count
	}
	if _, err := tx.ExecContext(ctx, `update loop_attempt_evidence set value_digest=?,snapshot_id=? where attempt_id=?`,
		frame.ValueDigest, snapshotID, attempt.ID); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_sources set admitted_attempt=?,
complete_attempt=case when ?='channels' then ? else complete_attempt end where id=?`, attempt.ID, coverage, attempt.ID, sourceID); err != nil {
		return 0, "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `update knowledge_locators set last_admitted=? where id=?`, attempt.ID, attempt.LocatorID); err != nil {
		return 0, "", nil, err
	}
	return sourceID, coverage, losses, nil
}

func insertLoopSnapshot(ctx context.Context, tx *sql.Tx, attempt KnowledgeAttempt, sourceID int64, frame LoopRawFrame, result loopcache.Result, outcome KnowledgeOutcome) (int64, error) {
	inserted, err := tx.ExecContext(ctx, `insert into loop_snapshots(locator_id,knowledge_source_id,value_digest,qualification,
file_id,sequence,latest_sequence,pending_operations,missing_blobs,partial,channel_count,tenant_id,user_id,
record_updated_at_at_admission,projected_at,first_attempt) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		attempt.LocatorID, sourceID, frame.ValueDigest, outcome.Qualification, result.FileID, result.Sequence, result.LatestSequence,
		result.PendingOperations, result.MissingBlobs, result.Partial, len(result.Channels), frame.TenantID, frame.UserID,
		knowledgeTime(frame.UpdatedAt), knowledgeTime(outcome.At), attempt.ID)
	if err != nil {
		return 0, err
	}
	snapshotID, err := inserted.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, loss := range result.Losses {
		if _, err := tx.ExecContext(ctx, `insert into loop_snapshot_losses(snapshot_id,code,count) values(?,?,?)
on conflict(snapshot_id,code) do update set count=count+excluded.count`, snapshotID, loss.Code, loss.Count); err != nil {
			return 0, err
		}
	}
	for ordinal, channel := range result.Channels {
		row, err := tx.ExecContext(ctx, `insert into loop_channels(snapshot_id,ordinal,path,sequence,text,markers,pending_operations)
values(?,?,?,?,?,?,?)`, snapshotID, ordinal, channel.Path, channel.Sequence, channel.Text, channel.Markers, channel.PendingOperations)
		if err != nil {
			return 0, err
		}
		channelID, err := row.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `insert into loop_fts(rowid,path,text) values(?,?,?)`, channelID, channel.Path, channel.Text); err != nil {
			return 0, err
		}
	}
	return snapshotID, nil
}
