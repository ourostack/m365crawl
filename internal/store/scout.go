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

// ScoutRun is one `scout sync` of one app folder under one stamp (scoutsessions.Stamp).
type ScoutRun struct {
	SourceID, RunID int64
	Stamp           string
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
// archive's last successful read of each of its files made under stamp; a file read under another
// stamp is read again.
func (s *Store) BeginScoutRun(ctx context.Context, platform, root, stamp string, at time.Time) (run ScoutRun, err error) {
	run.Stamp = stamp
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
where source_id=? and read_size is not null and read_stamp=?`, run.SourceID, stamp)
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
// observation, unchanged when the archive already holds these bytes under the run's stamp (or the
// file was not opened because its size, time and stamp match the last read), rescrubbed when it
// holds them under another stamp and stored them again in place from this read, or the file's
// state otherwise. Bytes seen before, even several versions ago, reuse that observation.
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
		var stamp string
		err = tx.QueryRowContext(ctx, `select id,stamp from scout_observations where file_id=? and digest=?`, fileID, in.Digest).Scan(&observation, &stamp)
		switch {
		case err == nil && stamp == run.Stamp:
			outcome = "unchanged"
		case err == nil:
			outcome = "rescrubbed"
			if err = rewriteScoutObservation(ctx, tx, observation, run.Stamp, in.Raw, in.Redactions, in.Session); err != nil {
				return "", err
			}
		case errors.Is(err, sql.ErrNoRows):
			outcome = "admitted"
			if observation, err = insertScoutObservation(ctx, tx, fileID, run, in, at); err != nil {
				return "", err
			}
		default:
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `update scout_files set latest_observation=?, read_size=?, read_modified_ns=?, read_stamp=? where id=?`,
			observation, in.Size, in.Modified.UnixNano(), run.Stamp, fileID); err != nil {
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

// scoutSpan is the earliest and latest known message time.
func scoutSpan(session *scoutsessions.Session) (first, last *time.Time) {
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
	return first, last
}

func insertScoutObservation(ctx context.Context, tx *sql.Tx, fileID int64, run ScoutRun, in ScoutFileInput, at time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `insert into scout_observations(file_id,digest,stamp,size,file_modified_at,captured_at,run_id,
native_id,name,model,origin,kind,device_claim,automation,created_raw,created_at,updated_raw,updated_at,message_count,first_message_at,
last_message_at,redactions,losses_json,raw_json) values(?,?,?,?,?,?,?,'','','','','','',0,'',null,'',null,0,null,null,0,'{}','')`,
		fileID, in.Digest, run.Stamp, in.Size, knowledgeTime(in.Modified), knowledgeTime(at), run.RunID)
	if err != nil {
		return 0, err
	}
	observation, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return observation, rewriteScoutObservation(ctx, tx, observation, run.Stamp, in.Raw, in.Redactions, in.Session)
}

// rewriteScoutObservation stores a session's scrubbed JSON, mapped fields and messages on an
// observation, keeping its digest, size, clocks and run. Messages keep their IDs by ordinal; a
// message the new mapping no longer has is removed with its FTS row. This is the in-place
// exception to immutability that a stamp change allows.
func rewriteScoutObservation(ctx context.Context, tx *sql.Tx, observation int64, stamp string, raw []byte, redactions int, session *scoutsessions.Session) error {
	first, last := scoutSpan(session)
	losses, _ := json.Marshal(session.Losses) // a map of strings to ints always encodes
	if _, err := tx.ExecContext(ctx, `update scout_observations set stamp=?,native_id=?,name=?,model=?,origin=?,kind=?,device_claim=?,automation=?,
created_raw=?,created_at=?,updated_raw=?,updated_at=?,message_count=?,first_message_at=?,last_message_at=?,redactions=?,losses_json=?,raw_json=?
where id=?`, stamp, session.NativeID, session.Name, session.Model, session.Origin, session.Kind, session.DeviceID, session.Automation,
		session.CreatedAtRaw, scoutOptionalTime(session.CreatedAt), session.UpdatedAtRaw, scoutOptionalTime(session.UpdatedAt),
		len(session.Messages), scoutOptionalTime(first), scoutOptionalTime(last), redactions, string(losses), string(raw), observation); err != nil {
		return err
	}
	existing := map[int]int64{}
	rows, err := tx.QueryContext(ctx, `select ordinal,id from scout_messages where observation_id=?`, observation)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ordinal int
		var id int64
		if err := rows.Scan(&ordinal, &id); err != nil {
			return errors.Join(err, rows.Close())
		}
		existing[ordinal] = id
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, m := range session.Messages {
		sum := sha256.Sum256([]byte(m.Role + "\x00" + m.NativeID + "\x00" + m.TimestampRaw + "\x00" + m.Text))
		values := []any{m.NativeID, m.Role, m.TimestampRaw, scoutOptionalTime(m.At), m.Text, m.ToolCalls, m.FileNames, m.Reasoning, hex.EncodeToString(sum[:])}
		id, ok := existing[m.Ordinal]
		delete(existing, m.Ordinal)
		if ok {
			if _, err := tx.ExecContext(ctx, `update scout_messages set native_id=?,role=?,at_raw=?,at=?,text=?,tool_calls=?,file_names=?,reasoning=?,digest=?
where id=?`, append(values, id)...); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `update scout_fts set name=?,text=? where rowid=?`, session.Name, m.Text, id); err != nil {
				return err
			}
			continue
		}
		result, err := tx.ExecContext(ctx, `insert into scout_messages(native_id,role,at_raw,at,text,tool_calls,file_names,reasoning,digest,
observation_id,ordinal) values(?,?,?,?,?,?,?,?,?,?,?)`, append(values, observation, m.Ordinal)...)
		if err != nil {
			return err
		}
		if id, err = result.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into scout_fts(rowid,name,text) values(?,?,?)`, id, session.Name, m.Text); err != nil {
			return err
		}
	}
	gone := make([]int64, 0, len(existing))
	for _, id := range existing {
		gone = append(gone, id)
	}
	slices.Sort(gone)
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `delete from scout_messages where id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from scout_fts where rowid=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ScoutRescrub counts what RescrubScout rewrote. Unparseable counts observations whose stored
// JSON no longer maps after scrubbing: they are blanked (no JSON, messages or index rows) under the
// new stamp, with loss rescrub_unparseable, so they cannot hold back later runs.
type ScoutRescrub struct {
	Rewritten, Unparseable int
}

// scoutMergePending is the meta key set with every re-scrub commit and cleared by
// FinishScoutRescrub once the FTS index is merged and the write-ahead log truncated, so an
// interrupted re-scrub still gets both later.
const scoutMergePending = "scout_rescrub_merge_pending"

// secureConn is a connection with secure_delete on, so text a re-scrub replaces is overwritten in
// the pages it frees. The connection keeps the setting when it returns to the pool, which only
// makes its later deletes overwrite their pages too.
func (s *Store) secureConn(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `pragma secure_delete=on`); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

// RescrubScout scrubs and maps again, in place, every observation stored under a stamp other than
// stamp, gone files included. It starts from the observation's stored (already scrubbed) JSON, so
// a rule added later reaches sessions the folder no longer has, and the observation's redactions
// add the new ones. Each observation commits on its own, with its new stamp, so memory holds one
// session, a full disk or a crash leaves each observation either under its old stamp or fully
// rewritten, and the next run resumes with the ones left. The counts cover what committed, also
// when an error stops the run. FinishScoutRescrub must follow.
func (s *Store) RescrubScout(ctx context.Context, stamp string, scrub func([]byte) ([]byte, int)) (out ScoutRescrub, err error) {
	conn, err := s.secureConn(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	rows, err := conn.QueryContext(ctx, `select id from scout_observations where stamp<>? order by id`, stamp)
	if err != nil {
		return out, err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return out, errors.Join(err, rows.Close())
		}
		stale = append(stale, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, id := range stale {
		unparseable, err := rescrubScoutObservation(ctx, conn, id, stamp, scrub)
		if err != nil {
			return out, err
		}
		out.Rewritten++
		if unparseable {
			out.Unparseable++
		}
	}
	return out, nil
}

func rescrubScoutObservation(ctx context.Context, conn *sql.Conn, id int64, stamp string, scrub func([]byte) ([]byte, int)) (unparseable bool, err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var raw string
	var redactions int
	if err = tx.QueryRowContext(ctx, `select raw_json,redactions from scout_observations where id=?`, id).Scan(&raw, &redactions); err != nil {
		return false, err
	}
	clean, n := scrub([]byte(raw))
	session, parseErr := scoutsessions.Parse(clean)
	if parseErr != nil {
		// Fail closed: keep nothing of a session that can no longer be scrubbed with certainty.
		unparseable, clean, n = true, nil, 0
		session = scoutsessions.Session{Losses: map[string]int{"rescrub_unparseable": 1}}
	}
	if err = rewriteScoutObservation(ctx, tx, id, stamp, clean, redactions+n, &session); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `insert into meta(key,value) values(?, '1') on conflict(key) do update set value='1'`, scoutMergePending); err != nil {
		return false, err
	}
	return unparseable, tx.Commit()
}

// FinishScoutRescrub completes a re-scrub, including one an earlier run left unfinished: it merges
// the FTS index (FTS5 keeps a replaced row's tokens until its segments merge) with secure_delete on,
// checkpoints and truncates the write-ahead log, then clears the pending mark. With nothing pending
// it only reads the mark. A checkpoint blocked by a concurrent reader is not an error: the mark is
// cleared and a later checkpoint truncates the log.
func (s *Store) FinishScoutRescrub(ctx context.Context) (err error) {
	var pending int
	if err = s.db.QueryRowContext(ctx, `select count(*) from meta where key=?`, scoutMergePending).Scan(&pending); err != nil || pending == 0 {
		return err
	}
	conn, err := s.secureConn(ctx)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `insert into scout_fts(scout_fts) values('optimize')`)
	if err = errors.Join(err, conn.Close()); err != nil {
		return err
	}
	var busy, frames, done int
	if err = s.db.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(&busy, &frames, &done); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `delete from meta where key=?`, scoutMergePending)
	return err
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
