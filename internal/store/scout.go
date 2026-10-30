package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutsessions"
)

// Scout file states as the archive records them for the last listing of a file.
const (
	ScoutArchived  = "archived"   // the archive holds the bytes the folder last had
	ScoutCloudOnly = "cloud_only" // OneDrive keeps the file only in the cloud; it was not read
	ScoutFailed    = "failed"
	ScoutUnsafe    = "unsafe"
	ScoutTooLarge  = "too_large"
)

// ScoutFileKey names one session file of an app folder.
type ScoutFileKey struct{ Device, Name string }

// ScoutKnown is the size and modification time of a file's last successful read.
type ScoutKnown struct{ Size, ModNanos int64 }

// ScoutRun is one `scout sync` of one app folder.
type ScoutRun struct {
	SourceID, RunID int64
	Known           map[ScoutFileKey]ScoutKnown
}

// ScoutFileInput is one listed file as the scan saw it. Session is set only for a file read and
// parsed; Raw is then its scrubbed JSON and Digest the SHA-256 of the bytes read.
type ScoutFileInput struct {
	Device, Name  string
	State, Reason string // read, unchanged, cloud_only, unsafe, too_large or failed, as scoutcapture reports
	Size          int64
	Modified      time.Time
	Digest        string
	Raw           []byte
	Redactions    int
	Session       *scoutsessions.Session
}

// ScoutRunEnd summarizes a finished scan for its run row and for marking files gone.
type ScoutRunEnd struct {
	Outcome, Reason string
	// DevicesListed says the folder's device folders were listed; only then can a file be gone.
	DevicesListed bool
	// SeenDevices are every device folder found; ListedDevices those whose files were listed.
	SeenDevices, ListedDevices                     []string
	Listed, Admitted, Unchanged, CloudOnly, Failed int
}

// ErrScoutSessionNotFound is a scout-session or observation ID the archive does not hold.
var ErrScoutSessionNotFound = errors.New("scout session not found")

// BeginScoutRun registers the app folder on first use, records a pending run and returns the
// archive's last successful read of each of its files.
func (s *Store) BeginScoutRun(ctx context.Context, platform, root string, at time.Time) (run ScoutRun, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `insert into scout_sources(platform,root,registered_at) values(?,?,?) on conflict(platform,root) do nothing`,
		platform, root, knowledgeTime(at)); err != nil {
		return run, err
	}
	if err = tx.QueryRowContext(ctx, `select id from scout_sources where platform=? and root=?`, platform, root).Scan(&run.SourceID); err != nil {
		return run, err
	}
	result, err := tx.ExecContext(ctx, `insert into scout_runs(source_id,started_at) values(?,?)`, run.SourceID, knowledgeTime(at))
	if err != nil {
		return run, err
	}
	if run.RunID, err = result.LastInsertId(); err != nil {
		return run, err
	}
	rows, err := tx.QueryContext(ctx, `select device_id,file_name,read_size,read_modified_ns from scout_files
where source_id=? and read_size is not null`, run.SourceID)
	if err != nil {
		return run, err
	}
	run.Known = map[ScoutFileKey]ScoutKnown{}
	for rows.Next() {
		var key ScoutFileKey
		var known ScoutKnown
		if err = rows.Scan(&key.Device, &key.Name, &known.Size, &known.ModNanos); err != nil {
			_ = rows.Close()
			return run, err
		}
		run.Known[key] = known
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return run, err
	}
	return run, tx.Commit()
}

// CommitScoutFile records one listed file in its own transaction. It returns admitted for a new
// observation, unchanged when the archive already holds these bytes (or the file was not opened
// because its size and time match the last read), or the file's state otherwise. An observation
// is never changed: bytes seen before, even several versions ago, reuse that observation.
func (s *Store) CommitScoutFile(ctx context.Context, run ScoutRun, in ScoutFileInput, at time.Time) (outcome string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	state, reason := in.State, in.Reason
	switch {
	case in.State == "read" && in.Session == nil:
		state, reason = ScoutFailed, "scout_session_unparsable"
	case in.State == "read", in.State == "unchanged":
		state, reason = ScoutArchived, ""
	}
	if _, err = tx.ExecContext(ctx, `insert into scout_files(source_id,device_id,file_name,state,reason,last_run,listed_at) values(?,?,?,?,?,?,?)
on conflict(source_id,device_id,file_name) do update set state=excluded.state, reason=excluded.reason, last_run=excluded.last_run,
 listed_at=excluded.listed_at, gone_at=null`, run.SourceID, in.Device, in.Name, state, reason, run.RunID, knowledgeTime(at)); err != nil {
		return "", err
	}
	var fileID int64
	if err = tx.QueryRowContext(ctx, `select id from scout_files where source_id=? and device_id=? and file_name=?`,
		run.SourceID, in.Device, in.Name).Scan(&fileID); err != nil {
		return "", err
	}
	outcome = state
	switch {
	case state == ScoutArchived && in.State == "unchanged":
		outcome = "unchanged"
	case state == ScoutArchived:
		var observation int64
		err = tx.QueryRowContext(ctx, `select id from scout_observations where file_id=? and digest=? and parser_version=?`,
			fileID, in.Digest, scoutsessions.ParserVersion).Scan(&observation)
		switch {
		case err == nil:
			outcome = "unchanged"
		case errors.Is(err, sql.ErrNoRows):
			outcome = "admitted"
			if observation, err = insertScoutObservation(ctx, tx, fileID, run.RunID, in, at); err != nil {
				return "", err
			}
		default:
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `update scout_files set latest_observation=?, read_size=?, read_modified_ns=? where id=?`,
			observation, in.Size, in.Modified.UnixNano(), fileID); err != nil {
			return "", err
		}
	}
	return outcome, tx.Commit()
}

func scoutOptionalTime(at *time.Time) any {
	if at == nil {
		return nil
	}
	return knowledgeTime(*at)
}

func insertScoutObservation(ctx context.Context, tx *sql.Tx, fileID, runID int64, in ScoutFileInput, at time.Time) (int64, error) {
	session := in.Session
	var first, last *time.Time
	for _, m := range session.Messages {
		if m.At == nil {
			continue
		}
		if first == nil || m.At.Before(*first) {
			first = m.At
		}
		if last == nil || m.At.After(*last) {
			last = m.At
		}
	}
	losses, _ := json.Marshal(session.Losses) // a map of strings to ints always encodes
	result, err := tx.ExecContext(ctx, `insert into scout_observations(file_id,digest,parser_version,size,file_modified_at,captured_at,run_id,
native_id,name,model,origin,kind,device_claim,automation,created_raw,created_at,updated_raw,updated_at,message_count,first_message_at,
last_message_at,redactions,losses_json,raw_json) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fileID, in.Digest, scoutsessions.ParserVersion, in.Size, knowledgeTime(in.Modified), knowledgeTime(at), runID,
		session.NativeID, session.Name, session.Model, session.Origin, session.Kind, session.DeviceID, session.Automation,
		session.CreatedAtRaw, scoutOptionalTime(session.CreatedAt), session.UpdatedAtRaw, scoutOptionalTime(session.UpdatedAt),
		len(session.Messages), scoutOptionalTime(first), scoutOptionalTime(last), in.Redactions, string(losses), string(in.Raw))
	if err != nil {
		return 0, err
	}
	observation, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, m := range session.Messages {
		sum := sha256.Sum256([]byte(m.Role + "\x00" + m.NativeID + "\x00" + m.TimestampRaw + "\x00" + m.Text))
		result, err := tx.ExecContext(ctx, `insert into scout_messages(observation_id,ordinal,native_id,role,at_raw,at,text,tool_calls,file_names,
reasoning,digest) values(?,?,?,?,?,?,?,?,?,?,?)`, observation, m.Ordinal, m.NativeID, m.Role, m.TimestampRaw, scoutOptionalTime(m.At), m.Text,
			m.ToolCalls, m.FileNames, m.Reasoning, hex.EncodeToString(sum[:]))
		if err != nil {
			return 0, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `insert into scout_fts(rowid,name,text) values(?,?,?)`, id, session.Name, m.Text); err != nil {
			return 0, err
		}
	}
	return observation, nil
}

// FinishScoutRun records the run's outcome and marks gone every file of a completely listed
// device folder that this run did not list, and every file of a device folder that is no longer
// there. A file in a device folder that could not be listed is left as it was. It returns how many
// files became gone.
func (s *Store) FinishScoutRun(ctx context.Context, run ScoutRun, end ScoutRunEnd, at time.Time) (gone int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if end.DevicesListed {
		rows, err := tx.QueryContext(ctx, `select id,device_id,last_run from scout_files where source_id=? and gone_at is null`, run.SourceID)
		if err != nil {
			return 0, err
		}
		var ids []int64
		for rows.Next() {
			var id, lastRun int64
			var device string
			if err = rows.Scan(&id, &device, &lastRun); err != nil {
				_ = rows.Close()
				return 0, err
			}
			removedDevice := !slices.Contains(end.SeenDevices, device)
			if lastRun != run.RunID && (removedDevice || slices.Contains(end.ListedDevices, device)) {
				ids = append(ids, id)
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return 0, err
		}
		for _, id := range ids {
			if _, err = tx.ExecContext(ctx, `update scout_files set gone_at=? where id=?`, knowledgeTime(at), id); err != nil {
				return 0, err
			}
		}
		gone = len(ids)
	}
	unreadable := len(end.SeenDevices) - len(end.ListedDevices)
	if _, err = tx.ExecContext(ctx, `update scout_runs set finished_at=?, outcome=?, reason=?, devices=?, unreadable_devices=?, listed=?, admitted=?,
unchanged=?, cloud_only=?, failed=?, gone=? where id=?`, knowledgeTime(at), end.Outcome, end.Reason, len(end.SeenDevices), unreadable, end.Listed,
		end.Admitted, end.Unchanged, end.CloudOnly, end.Failed, gone, run.RunID); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `update scout_sources set last_run=? where id=?`, run.RunID, run.SourceID); err != nil {
		return 0, err
	}
	return gone, tx.Commit()
}
