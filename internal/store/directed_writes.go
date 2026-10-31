package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// DirectedWriteSchemaVersion is the archive schema that added the directed write log.
const DirectedWriteSchemaVersion = 26

// DirectedDraftSchemaVersion is the archive schema that added the reply drafts of directed writes.
const DirectedDraftSchemaVersion = 28

// DirectedReactionSchemaVersion is the archive schema that added directed_writes.reaction.
const DirectedReactionSchemaVersion = 30

// The write log is append/update-only: no row of either table is ever deleted, and an event is
// never changed.
const directedWriteSchemaDDL = `
create table if not exists directed_writes(
 id integer primary key autoincrement,
 created_at text not null, updated_at text not null,
 contract text not null, action text not null,
 effect text not null check(effect in ('only_you','visible_to_others')),
 agent text not null, reason text not null, identity text not null,
 conversation text not null default '', message text not null default '',
 recipients_json text not null default '[]', subject text not null default '', text text not null default '',
 intent_key text not null default '', expected_version text not null default '',
 user_id text not null, tenant_id text not null default '', account_pin text not null default '',
 outcome text not null check(outcome in ('queued','sent','failed','unknown')),
 phase text not null check(phase in ('in_flight','answered','verified','settled')),
 verification text not null default '' check(verification in ('','found','not_sent_proven','person')),
 server_id text not null default '', server_status integer not null default 0,
 watermark_id text not null default '', watermark_at text not null default '',
 error_code text not null default '', m365crawl_version text not null default '',
 reaction text not null default ''
);
create unique index if not exists directed_writes_intent on directed_writes(intent_key) where intent_key<>'' and outcome<>'failed';
create unique index if not exists directed_writes_server on directed_writes(action,conversation,server_id) where server_id<>'';
create trigger if not exists directed_writes_no_delete before delete on directed_writes
begin select raise(abort,'directed_writes rows are never deleted'); end;
create table if not exists directed_write_drafts(
 write_id integer primary key references directed_writes(id),
 draft_id text not null, internet_message_id text not null, recorded_at text not null
);
create trigger if not exists directed_write_drafts_no_delete before delete on directed_write_drafts
begin select raise(abort,'directed_write_drafts rows are never deleted'); end;
create trigger if not exists directed_write_drafts_no_update before update on directed_write_drafts
begin select raise(abort,'directed_write_drafts rows are never changed'); end;
create table if not exists directed_write_events(
 id integer primary key autoincrement,
 write_id integer not null references directed_writes(id),
 at text not null, outcome text not null, phase text not null,
 verification text not null default '', cause text not null
);
create trigger if not exists directed_write_events_no_update before update on directed_write_events
begin select raise(abort,'directed_write_events is append-only'); end;
create trigger if not exists directed_write_events_no_delete before delete on directed_write_events
begin select raise(abort,'directed_write_events is append-only'); end;
`

// DirectedWrite is one accepted directed request and what became of it.
type DirectedWrite struct {
	ID                           int64
	CreatedAt, UpdatedAt         time.Time
	Contract, Action, Effect     string
	Agent, Reason, Identity      string
	Conversation, Message        string
	Recipients                   []string
	Subject, Text, IntentKey     string
	ExpectedVersion              string
	UserID, TenantID, AccountPin string
	Outcome, Phase, Verification string
	ServerID                     string
	ServerStatus                 int
	WatermarkID, WatermarkAt     string
	ErrorCode, Version           string
	Reaction                     string
}

// DirectedWriteChange is a later state of a write and why it changed.
type DirectedWriteChange struct {
	Outcome, Phase, Verification, ServerID string
	ServerStatus                           int
	ErrorCode, Cause                       string
}

// directedWriteNow is the log's clock; a test seam.
var directedWriteNow = time.Now

func hasDirectedWrites(ctx context.Context, q rowQuerier) (bool, error) {
	return hasFeature(ctx, q, directedWriteFeature)
}

const directedWriteColumns = `id,created_at,updated_at,contract,action,effect,agent,reason,identity,conversation,message,
recipients_json,subject,text,intent_key,expected_version,user_id,tenant_id,account_pin,outcome,phase,verification,
server_id,server_status,watermark_id,watermark_at,error_code,m365crawl_version`

// directedColumns is directedWriteColumns with the reaction column, read as empty on an archive
// without it, which a read-only open does not migrate.
func directedColumns(ctx context.Context, q rowQuerier) (string, error) {
	has, err := hasFeature(ctx, q, directedReactionFeature)
	if !has {
		return directedWriteColumns + `,'' as reaction`, err
	}
	return directedWriteColumns + `,reaction`, nil
}

func scanDirectedWrite(rs interface{ Scan(...any) error }) (DirectedWrite, error) {
	var w DirectedWrite
	var created, updated, recipients string
	err := rs.Scan(&w.ID, &created, &updated, &w.Contract, &w.Action, &w.Effect, &w.Agent, &w.Reason, &w.Identity,
		&w.Conversation, &w.Message, &recipients, &w.Subject, &w.Text, &w.IntentKey, &w.ExpectedVersion, &w.UserID,
		&w.TenantID, &w.AccountPin, &w.Outcome, &w.Phase, &w.Verification, &w.ServerID, &w.ServerStatus,
		&w.WatermarkID, &w.WatermarkAt, &w.ErrorCode, &w.Version, &w.Reaction)
	if err != nil {
		return DirectedWrite{}, err
	}
	w.CreatedAt, _ = time.Parse(timeLayout, created)
	w.UpdatedAt, _ = time.Parse(timeLayout, updated)
	if json.Unmarshal([]byte(recipients), &w.Recipients) != nil {
		return DirectedWrite{}, errors.New("directed write recipients are not a JSON list")
	}
	return w, nil
}

type directedQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func directedWrites(ctx context.Context, q directedQuerier, limit int) ([]DirectedWrite, error) {
	ok, err := hasDirectedWrites(ctx, q)
	if err != nil || !ok {
		return nil, err
	}
	if limit <= 0 {
		limit = -1
	}
	columns, err := directedColumns(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `select `+columns+` from directed_writes order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DirectedWrite
	for rows.Next() {
		w, err := scanDirectedWrite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DirectedWrites lists the newest writes first, at most limit (all when limit is not positive).
// An archive below schema 26 has none.
func (s *Store) DirectedWrites(ctx context.Context, limit int) ([]DirectedWrite, error) {
	return directedWrites(ctx, s.db, limit)
}

// DirectedWriteByID reads one write; ok is false when there is none.
func (s *Store) DirectedWriteByID(ctx context.Context, id int64) (DirectedWrite, bool, error) {
	has, err := hasDirectedWrites(ctx, s.db)
	if err != nil || !has {
		return DirectedWrite{}, false, err
	}
	columns, err := directedColumns(ctx, s.db)
	if err != nil {
		return DirectedWrite{}, false, err
	}
	w, err := scanDirectedWrite(s.db.QueryRowContext(ctx, `select `+columns+` from directed_writes where id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DirectedWrite{}, false, nil
	}
	return w, err == nil, err
}

// BeginDirectedWrite records an accepted write before it acts. In one transaction it gives gate
// every write committed so far, newest first, and inserts w only when gate returns nil, so a
// decision made against those rows cannot race another writer's insert.
func (s *Store) BeginDirectedWrite(ctx context.Context, w DirectedWrite, gate func([]DirectedWrite) error) (id int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	ok, err := hasDirectedWrites(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("the archive has no directed write log")
	}
	earlier, err := directedWrites(ctx, tx, 0)
	if err != nil {
		return 0, err
	}
	if err = gate(earlier); err != nil {
		return 0, err
	}
	recipients, _ := json.Marshal(append([]string{}, w.Recipients...))
	now := directedWriteNow().UTC().Format(timeLayout)
	res, err := tx.ExecContext(ctx, `insert into directed_writes(created_at,updated_at,contract,action,effect,agent,reason,
identity,conversation,message,recipients_json,subject,text,intent_key,expected_version,user_id,tenant_id,account_pin,
outcome,phase,verification,server_id,server_status,watermark_id,watermark_at,error_code,m365crawl_version,reaction)
values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, now, now, w.Contract, w.Action, w.Effect, w.Agent,
		w.Reason, w.Identity, w.Conversation, w.Message, string(recipients), w.Subject, w.Text, w.IntentKey,
		w.ExpectedVersion, w.UserID, w.TenantID, w.AccountPin, w.Outcome, w.Phase, w.Verification, w.ServerID,
		w.ServerStatus, w.WatermarkID, w.WatermarkAt, w.ErrorCode, w.Version, w.Reaction)
	if err != nil {
		return 0, err
	}
	id, _ = res.LastInsertId()
	if _, err = tx.ExecContext(ctx, `insert into directed_write_events(write_id,at,outcome,phase,verification,cause)
values(?,?,?,?,?,'accepted')`, id, now, w.Outcome, w.Phase, w.Verification); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateDirectedWrite moves a write from the outcome and phase it was read in to change, and
// records the event. changed is false, and nothing is written, when the write has moved on.
func (s *Store) UpdateDirectedWrite(ctx context.Context, id int64, fromOutcome, fromPhase string, change DirectedWriteChange) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil || !changed {
			_ = tx.Rollback()
		}
	}()
	now := directedWriteNow().UTC().Format(timeLayout)
	res, err := tx.ExecContext(ctx, `update directed_writes set updated_at=?,outcome=?,phase=?,verification=?,
server_id=case when ?<>'' then ? else server_id end,server_status=case when ?<>0 then ? else server_status end,
error_code=? where id=? and outcome=? and phase=?`, now, change.Outcome, change.Phase, change.Verification,
		change.ServerID, change.ServerID, change.ServerStatus, change.ServerStatus, change.ErrorCode, id, fromOutcome, fromPhase)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `insert into directed_write_events(write_id,at,outcome,phase,verification,cause)
values(?,?,?,?,?,?)`, id, now, change.Outcome, change.Phase, change.Verification, change.Cause); err != nil {
		return false, err
	}
	changed = true
	return true, tx.Commit()
}

func hasDirectedDrafts(ctx context.Context, q rowQuerier) (bool, error) {
	return hasFeature(ctx, q, directedDraftFeature)
}

// RecordDirectedDraft records the reply draft a write made, before the write may send it. It
// applies only while the write is still unknown and in flight; recorded is false otherwise.
func (s *Store) RecordDirectedDraft(ctx context.Context, id int64, draftID, internetMessageID string) (recorded bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil || !recorded {
			_ = tx.Rollback()
		}
	}()
	now := directedWriteNow().UTC().Format(timeLayout)
	res, err := tx.ExecContext(ctx, `insert into directed_write_drafts(write_id,draft_id,internet_message_id,recorded_at)
select id,?,?,? from directed_writes where id=? and outcome='unknown' and phase='in_flight'`, draftID, internetMessageID, now, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `insert into directed_write_events(write_id,at,outcome,phase,verification,cause)
values(?,?,'unknown','in_flight','','drafted')`, id, now); err != nil {
		return false, err
	}
	recorded = true
	return true, tx.Commit()
}

// DirectedDraft reads the reply draft a write recorded; ok is false when it recorded none.
func (s *Store) DirectedDraft(ctx context.Context, id int64) (draftID, internetMessageID string, ok bool, err error) {
	if has, err := hasDirectedDrafts(ctx, s.db); err != nil || !has {
		return "", "", false, err
	}
	err = s.db.QueryRowContext(ctx, `select draft_id,internet_message_id from directed_write_drafts where write_id=?`, id).Scan(&draftID, &internetMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return draftID, internetMessageID, err == nil, err
}

// DirectedDraftHeld says a directed send recorded draftID and has not failed: the draft is the
// evidence of that send and may be sent by it, so no other write may change or discard it.
func (s *Store) DirectedDraftHeld(ctx context.Context, draftID string) (bool, error) {
	if has, err := hasDirectedDrafts(ctx, s.db); err != nil || !has {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from directed_write_drafts d join directed_writes w on w.id=d.write_id
where d.draft_id=? and w.outcome<>'failed'`, draftID).Scan(&n)
	return n > 0, err
}
