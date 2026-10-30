package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// ScoutRunRow is one recorded scan of an app folder.
type ScoutRunRow struct {
	ID                                                                               int64
	StartedAt                                                                        time.Time
	FinishedAt                                                                       *time.Time
	Outcome, Reason                                                                  string
	Devices, UnreadableDevices, Listed, Admitted, Unchanged, CloudOnly, Failed, Gone int
}

// ScoutFileCounts counts a source's files by their state at the last listing. Present excludes
// gone files; Sessions counts the files the archive holds at least one read of, gone ones included.
type ScoutFileCounts struct {
	Present, Archived, CloudOnly, Failed, Gone, Sessions int
}

// ScoutSource is one registered app folder with its last run and current file counts.
type ScoutSource struct {
	ID           int64
	Platform     string
	Root         string
	RegisteredAt time.Time
	LastRun      *ScoutRunRow
	Devices      int
	Files        ScoutFileCounts
}

// ScoutObservation is one immutable read of a session file.
type ScoutObservation struct {
	ID                                               int64
	Digest                                           string
	Size                                             int64
	FileModifiedAt, CapturedAt                       time.Time
	NativeID, Name, Model, Origin, Kind, DeviceClaim string
	Automation                                       bool
	CreatedRaw, UpdatedRaw                           string
	CreatedAt, UpdatedAt                             *time.Time
	MessageCount                                     int
	FirstMessageAt, LastMessageAt                    *time.Time
	Redactions                                       int
	Losses                                           map[string]int
}

// ScoutSession is one session file with its latest (or a selected) observation.
type ScoutSession struct {
	FileID, SourceID  int64
	Device, FileName  string
	State, Reason     string
	GoneAt            *time.Time
	Observations      int
	LatestObservation int64
	Observation       ScoutObservation
}

// ScoutMessage is one archived message of an observation.
type ScoutMessage struct {
	ID                   int64
	Ordinal              int
	NativeID, Role       string
	AtRaw                string
	At                   *time.Time
	Text                 string
	ToolCalls, FileNames int
	Reasoning            bool
}

// ScoutHit is a matching message with the session it belongs to.
type ScoutHit struct {
	Message ScoutMessage
	Session ScoutSession
}

// ScoutDetail is one session observation with a page of its messages.
type ScoutDetail struct {
	Session       ScoutSession
	Messages      []ScoutMessage
	MessagesTotal int
}

// ScoutFilter narrows Scout reads. A zero field does not filter; Limit zero returns everything.
type ScoutFilter struct {
	SourceID     int64
	Device       string
	Automation   *bool
	Since, Until time.Time
	Limit        int
}

// scoutPresent reports whether the archive has the Scout tables: an archive written before schema
// 21 and opened read-only has none, and reads it as holding no Scout source.
func scoutPresent(ctx context.Context, reader knowledgeReader) (bool, error) {
	var n int
	err := reader.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='scout_files'`).Scan(&n)
	return n == 1, err
}

func scoutTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, errors.New("scout: invalid stored timestamp")
	}
	at = at.UTC()
	return &at, nil
}

// scoutClock is one stored timestamp and where it goes: optional (nil when NULL) or required.
type scoutClock struct {
	raw      sql.NullString
	optional **time.Time
	required *time.Time
}

// scoutTimes parses each stored timestamp into its target and stops at the first invalid one.
func scoutTimes(clocks ...scoutClock) error {
	for _, c := range clocks {
		at, err := scoutTime(c.raw)
		if err != nil {
			return err
		}
		if c.optional != nil {
			*c.optional = at
		} else if at != nil {
			*c.required = *at
		}
	}
	return nil
}

// ScoutSources reads every registered app folder in ID order. ok is false for an archive without
// the Scout tables.
func (s *Store) ScoutSources(ctx context.Context) (sources []ScoutSource, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	return readScoutSources(ctx, tx)
}

func readScoutSources(ctx context.Context, reader knowledgeReader) ([]ScoutSource, bool, error) {
	ok, err := scoutPresent(ctx, reader)
	if err != nil || !ok {
		return []ScoutSource{}, false, err
	}
	rows, err := reader.QueryContext(ctx, `select s.id,s.platform,s.root,s.registered_at,
 r.id,r.started_at,r.finished_at,r.outcome,r.reason,r.devices,r.unreadable_devices,r.listed,r.admitted,r.unchanged,r.cloud_only,r.failed,r.gone,
 (select count(distinct device_id) from scout_files f where f.source_id=s.id and f.gone_at is null),
 (select count(*) from scout_files f where f.source_id=s.id and f.gone_at is null),
 (select count(*) from scout_files f where f.source_id=s.id and f.gone_at is null and f.state='archived'),
 (select count(*) from scout_files f where f.source_id=s.id and f.gone_at is null and f.state='cloud_only'),
 (select count(*) from scout_files f where f.source_id=s.id and f.gone_at is null and f.state not in ('archived','cloud_only')),
 (select count(*) from scout_files f where f.source_id=s.id and f.gone_at is not null),
 (select count(*) from scout_files f where f.source_id=s.id and f.latest_observation is not null)
from scout_sources s left join scout_runs r on r.id=s.last_run order by s.id`)
	if err != nil {
		return nil, false, err
	}
	out := []ScoutSource{}
	for rows.Next() {
		var src ScoutSource
		var registered, started, finished, outcome, reason sql.NullString
		var runID sql.NullInt64
		var run ScoutRunRow
		var counts [8]sql.NullInt64
		if err := rows.Scan(&src.ID, &src.Platform, &src.Root, &registered, &runID, &started, &finished, &outcome, &reason,
			&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7],
			&src.Devices, &src.Files.Present, &src.Files.Archived, &src.Files.CloudOnly, &src.Files.Failed, &src.Files.Gone, &src.Files.Sessions); err != nil {
			return nil, false, errors.Join(err, rows.Close())
		}
		if err := scoutTimes(scoutClock{raw: registered, required: &src.RegisteredAt}); err != nil {
			return nil, false, errors.Join(err, rows.Close())
		}
		if runID.Valid {
			run.ID, run.Outcome, run.Reason = runID.Int64, outcome.String, reason.String
			if err := scoutTimes(scoutClock{raw: started, required: &run.StartedAt}, scoutClock{raw: finished, optional: &run.FinishedAt}); err != nil {
				return nil, false, errors.Join(err, rows.Close())
			}
			ints := []*int{&run.Devices, &run.UnreadableDevices, &run.Listed, &run.Admitted, &run.Unchanged, &run.CloudOnly, &run.Failed, &run.Gone}
			for i, target := range ints {
				*target = int(counts[i].Int64)
			}
			src.LastRun = &run
		}
		out = append(out, src)
	}
	return out, true, errors.Join(rows.Err(), rows.Close())
}

const scoutSessionColumns = `f.id,f.source_id,f.device_id,f.file_name,f.state,f.reason,f.gone_at,f.latest_observation,
 (select count(*) from scout_observations x where x.file_id=f.id),
 o.id,o.digest,o.size,o.file_modified_at,o.captured_at,o.native_id,o.name,o.model,o.origin,o.kind,o.device_claim,o.automation,
 o.created_raw,o.created_at,o.updated_raw,o.updated_at,o.message_count,o.first_message_at,o.last_message_at,o.redactions,o.losses_json`

// The fixed parts of the Scout queries; the filters between them add only bound parameters.
const (
	scoutSessionsSQL   = `select ` + scoutSessionColumns + ` from scout_files f join scout_observations o on o.id=f.latest_observation where 1=1`
	scoutSessionsOrder = ` order by ` + scoutActivity + ` desc, f.id desc`
	scoutSearchSQL     = `with hits as (select ` + scoutSessionColumns + `,
 m.id as message_id,m.ordinal,m.native_id as message_native,m.role,m.at_raw,m.at,m.text,m.tool_calls,m.file_names,m.reasoning,
 row_number() over (partition by o.file_id,m.digest order by o.id desc) as copy
 from scout_messages m join scout_observations o on o.id=m.observation_id join scout_files f on f.id=o.file_id
 where m.id in (select rowid from scout_fts where scout_fts match ?)`
	scoutSearchOrder = `) select * from hits where copy=1 order by at is null, at desc, message_id desc`
	scoutDetailSQL   = `select ` + scoutSessionColumns + ` from scout_files f join scout_observations o
 on o.file_id=f.id and o.id=(case when ?=0 then f.latest_observation else ? end) where f.id=?`
	scoutPageSQL = `select id,ordinal,native_id,role,at_raw,at,text,tool_calls,file_names,reasoning from scout_messages where observation_id=? order by ordinal`
)

// scoutActivity is the session's latest known time: its last message, else its own updated time,
// else the file's modification time, which is always known.
const scoutActivity = `coalesce(o.last_message_at,o.updated_at,o.file_modified_at)`

type scoutScanner interface{ Scan(...any) error }

func scanScoutSession(row scoutScanner, extra ...any) (ScoutSession, error) {
	var out ScoutSession
	o := &out.Observation
	var gone, modified, captured, created, updated, first, last sql.NullString
	var latest sql.NullInt64
	var losses string
	dest := append([]any{&out.FileID, &out.SourceID, &out.Device, &out.FileName, &out.State, &out.Reason, &gone, &latest, &out.Observations,
		&o.ID, &o.Digest, &o.Size, &modified, &captured, &o.NativeID, &o.Name, &o.Model, &o.Origin, &o.Kind, &o.DeviceClaim, &o.Automation,
		&o.CreatedRaw, &created, &o.UpdatedRaw, &updated, &o.MessageCount, &first, &last, &o.Redactions, &losses}, extra...)
	if err := row.Scan(dest...); err != nil {
		return out, err
	}
	out.LatestObservation = latest.Int64
	if err := scoutTimes(scoutClock{raw: gone, optional: &out.GoneAt}, scoutClock{raw: modified, required: &o.FileModifiedAt},
		scoutClock{raw: captured, required: &o.CapturedAt}, scoutClock{raw: created, optional: &o.CreatedAt}, scoutClock{raw: updated, optional: &o.UpdatedAt},
		scoutClock{raw: first, optional: &o.FirstMessageAt}, scoutClock{raw: last, optional: &o.LastMessageAt}); err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(losses), &o.Losses); err != nil {
		return out, errors.New("scout: invalid stored losses")
	}
	return out, nil
}

func scoutWhere(f ScoutFilter, timeColumn, startColumn string) (string, []any) {
	where, args := "", []any{}
	if f.SourceID > 0 {
		where += " and f.source_id=?"
		args = append(args, f.SourceID)
	}
	if f.Device != "" {
		where += " and f.device_id=?"
		args = append(args, f.Device)
	}
	if f.Automation != nil {
		where += " and o.automation=?"
		args = append(args, *f.Automation)
	}
	if !f.Since.IsZero() {
		where += " and " + timeColumn + ">=?"
		args = append(args, knowledgeTime(f.Since))
	}
	if !f.Until.IsZero() {
		where += " and " + startColumn + "<?"
		args = append(args, knowledgeTime(f.Until))
	}
	return where, args
}

func scoutLimit(f ScoutFilter, args []any) (string, []any) {
	if f.Limit <= 0 {
		return "", args
	}
	return " limit ?", append(args, f.Limit+1)
}

// ScoutSessions lists each session file's latest observation, most recent activity first. A
// session is in a window when its activity reaches the start and it began before the end.
func (s *Store) ScoutSessions(ctx context.Context, f ScoutFilter) (sessions []ScoutSession, truncated bool, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if ok, err := scoutPresent(ctx, tx); err != nil || !ok {
		return []ScoutSession{}, false, err
	}
	where, args := scoutWhere(f, scoutActivity, `coalesce(o.first_message_at,o.created_at,`+scoutActivity+`)`)
	limit, args := scoutLimit(f, args)
	rows, err := tx.QueryContext(ctx, scoutSessionsSQL+where+scoutSessionsOrder+limit, args...) // #nosec G202 -- fixed clauses; every value is a bound parameter.
	if err != nil {
		return nil, false, err
	}
	out := []ScoutSession{}
	for rows.Next() {
		session, err := scanScoutSession(rows)
		if err != nil {
			return nil, false, errors.Join(err, rows.Close())
		}
		out = append(out, session)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	if f.Limit > 0 && len(out) > f.Limit {
		return out[:f.Limit], true, nil
	}
	return out, false, nil
}

// ScoutSearch finds archived messages matching query. A message kept unchanged by several
// observations of the same file is returned once, from the newest of them. Messages with a known
// time come first, newest first; bounded windows exclude messages with no known time.
func (s *Store) ScoutSearch(ctx context.Context, query string, f ScoutFilter) (hits []ScoutHit, truncated bool, err error) {
	match := buildFTSQuery(query)
	if match == "" {
		return nil, false, errs.Usage("scout search requires searchable words")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if ok, err := scoutPresent(ctx, tx); err != nil || !ok {
		return []ScoutHit{}, false, err
	}
	where, args := scoutWhere(f, "m.at", "m.at")
	args = append([]any{match}, args...)
	limit, args := scoutLimit(f, args)
	rows, err := tx.QueryContext(ctx, scoutSearchSQL+where+scoutSearchOrder+limit, args...) // #nosec G202 -- fixed clauses; every value is a bound parameter.
	if err != nil {
		return nil, false, err
	}
	out := []ScoutHit{}
	for rows.Next() {
		var hit ScoutHit
		var at sql.NullString
		var copyNumber int
		m := &hit.Message
		hit.Session, err = scanScoutSession(rows, &m.ID, &m.Ordinal, &m.NativeID, &m.Role, &m.AtRaw, &at, &m.Text, &m.ToolCalls, &m.FileNames, &m.Reasoning, &copyNumber)
		if err == nil {
			err = scoutTimes(scoutClock{raw: at, optional: &m.At})
		}
		if err != nil {
			return nil, false, errors.Join(err, rows.Close())
		}
		out = append(out, hit)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	if f.Limit > 0 && len(out) > f.Limit {
		return out[:f.Limit], true, nil
	}
	return out, false, nil
}

// ScoutSessionDetail reads one session file with one observation (its latest when observationID
// is zero) and the page of that observation's messages that starts at offset; limit zero returns
// them all. An unknown file or an observation of another file is ErrScoutSessionNotFound.
func (s *Store) ScoutSessionDetail(ctx context.Context, fileID, observationID int64, offset, limit int) (ScoutDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ScoutDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if ok, err := scoutPresent(ctx, tx); err != nil || !ok {
		return ScoutDetail{}, errors.Join(err, ErrScoutSessionNotFound)
	}
	row := tx.QueryRowContext(ctx, scoutDetailSQL, observationID, observationID, fileID)
	session, err := scanScoutSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ScoutDetail{}, ErrScoutSessionNotFound
	}
	if err != nil {
		return ScoutDetail{}, err
	}
	out := ScoutDetail{Session: session, Messages: []ScoutMessage{}}
	if err := tx.QueryRowContext(ctx, `select count(*) from scout_messages where observation_id=?`, session.Observation.ID).Scan(&out.MessagesTotal); err != nil {
		return ScoutDetail{}, err
	}
	page := " limit -1 offset ?"
	args := []any{session.Observation.ID}
	if limit > 0 {
		page = " limit ? offset ?"
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, scoutPageSQL+page, append(args, offset)...) // #nosec G202 -- a fixed paging clause; values are bound parameters.
	if err != nil {
		return ScoutDetail{}, err
	}
	for rows.Next() {
		var m ScoutMessage
		var at sql.NullString
		err := rows.Scan(&m.ID, &m.Ordinal, &m.NativeID, &m.Role, &m.AtRaw, &at, &m.Text, &m.ToolCalls, &m.FileNames, &m.Reasoning)
		if err == nil {
			err = scoutTimes(scoutClock{raw: at, optional: &m.At})
		}
		if err != nil {
			return ScoutDetail{}, errors.Join(err, rows.Close())
		}
		out.Messages = append(out.Messages, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return ScoutDetail{}, err
	}
	return out, nil
}
