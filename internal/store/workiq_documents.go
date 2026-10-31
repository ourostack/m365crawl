package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// WorkIQDocumentSchemaVersion is the archive schema that added explicitly fetched WorkIQ
// documents (version 23). An archive below it has no document state to read.
const WorkIQDocumentSchemaVersion = 23

// WorkIQDocumentQualification names the admission rules a check was judged under.
const WorkIQDocumentQualification = "workiq-document-v1"

// A document is one drive item per WorkIQ account. Every explicit fetch appends a check; only an
// admitted check has a body, and a later refused check deletes the body and its search row. The
// index runs in FTS5 secure-delete mode, so a deleted row's words leave its segments at once rather
// than at the next merge.
const workiqDocumentSchemaDDL = `
create table if not exists workiq_documents(
 id integer primary key autoincrement,
 account_id text not null, drive_id text not null, item_id text not null,
 name text not null, web_url text not null,
 first_checked_at text not null, last_checked_at text not null,
 last_check_id integer, body_check_id integer,
 unique(account_id,drive_id,item_id)
);
create table if not exists workiq_document_checks(
 id integer primary key autoincrement,
 document_id integer not null references workiq_documents(id),
 checked_at text not null,
 outcome text not null check(outcome in ('admitted','refused')),
 code text not null default '',
 content_protection text,
 label_id text, label_name text, label_encrypted integer check(label_encrypted in (0,1)),
 attribution text not null default '' check(attribution in ('','label','none')),
 bytes_state text not null default 'not_inspected'
  check(bytes_state in ('not_inspected','not_encrypted','encrypted','unknown','unavailable')),
 rights_source text not null default 'workiq_retrieve',
 qualification text not null
);
create index if not exists workiq_document_checks_document on workiq_document_checks(document_id,id);
create table if not exists workiq_document_bodies(
 document_id integer primary key references workiq_documents(id),
 check_id integer not null references workiq_document_checks(id),
 text text not null, digest text not null,
 extracts integer not null check(extracts>=0), truncated integer not null check(truncated in (0,1))
);
create virtual table if not exists workiq_document_fts using fts5(title,text);
insert into workiq_document_fts(workiq_document_fts,rank) select 'secure-delete',1
 where not exists(select 1 from workiq_document_fts_config where k='secure-delete' and v=1);
create trigger if not exists workiq_document_bodies_fts_insert after insert on workiq_document_bodies begin
 insert into workiq_document_fts(rowid,title,text) select new.document_id,d.name,new.text from workiq_documents d where d.id=new.document_id;
end;
create trigger if not exists workiq_document_bodies_fts_delete after delete on workiq_document_bodies begin
 delete from workiq_document_fts where rowid=old.document_id;
end;
`

// WorkIQDocumentCheck is one explicit fetch's judgment of a document.
type WorkIQDocumentCheck struct {
	ID                int64     `json:"check_id"`
	CheckedAt         time.Time `json:"checked_at"`
	Outcome           string    `json:"outcome"`
	Code              string    `json:"code,omitempty"`
	ContentProtection *string   `json:"content_protection"`
	LabelID           *string   `json:"label_id"`
	LabelName         *string   `json:"label_name"`
	LabelEncrypted    *bool     `json:"label_encrypted"`
	Attribution       string    `json:"protection_attribution,omitempty"`
	BytesState        string    `json:"bytes_state"`
	RightsSource      string    `json:"rights_source"`
	Qualification     string    `json:"qualification"`
}

// WorkIQDocumentBody is the admitted text: only the excerpts WorkIQ retrieve released.
type WorkIQDocumentBody struct {
	CheckID   int64  `json:"check_id"`
	Text      string `json:"text"`
	Digest    string `json:"digest"`
	Extracts  int    `json:"extracts"`
	Truncated bool   `json:"truncated"`
}

// WorkIQDocument is a document's identity, its latest check and its retained body, if any.
type WorkIQDocument struct {
	ID             int64               `json:"-"`
	Account        string              `json:"account"`
	DriveID        string              `json:"drive_id"`
	ItemID         string              `json:"item_id"`
	Name           string              `json:"name"`
	WebURL         string              `json:"web_url"`
	FirstCheckedAt time.Time           `json:"first_checked_at"`
	LastCheckedAt  time.Time           `json:"last_checked_at"`
	Last           WorkIQDocumentCheck `json:"last_check"`
	Body           *WorkIQDocumentBody `json:"-"`
}

// WorkIQDocumentRecord is what an explicit fetch writes.
type WorkIQDocumentRecord struct {
	Account, DriveID, ItemID, Name, WebURL string
	Check                                  WorkIQDocumentCheck
	Text                                   string // admitted checks only
	Extracts                               int
	Truncated                              bool
}

// WorkIQDocumentWrite says what a record changed.
type WorkIQDocumentWrite struct {
	DocumentID  int64
	CheckID     int64
	BodyDeleted bool // a previously admitted body was deleted by this refusal
	// LogPending says replaced or deleted text may still be in the write-ahead log, because a reader
	// kept the checkpoint from truncating it; the next sync clears it (ClearPurgedLog).
	LogPending bool
}

var ErrWorkIQDocumentUnknown = errors.New("workiq document not found")

// afterWorkIQDocumentCommit runs between a document check's commit and its log clear; a test seam.
var afterWorkIQDocumentCommit = func() {}

// hasWorkIQDocuments says whether the archive's schema has document state; q is the database or
// the transaction that will use it.
func hasWorkIQDocuments(ctx context.Context, q rowQuerier) (bool, error) {
	return hasFeature(ctx, q, workIQDocumentFeature)
}

func documentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validDocumentRecord(r WorkIQDocumentRecord) bool {
	c := r.Check
	return r.Account != "" && r.DriveID != "" && r.ItemID != "" && r.Name != "" && r.WebURL != "" && !c.CheckedAt.IsZero() &&
		(c.Outcome == "admitted" && c.Code == "" || c.Outcome == "refused" && c.Code != "" && r.Text == "") &&
		c.Qualification != "" && c.RightsSource != ""
}

// RecordWorkIQDocument appends one check in a single transaction. An admitted check replaces the
// body (and, by trigger, its search row); a refused check deletes any body the document had.
func (s *Store) RecordWorkIQDocument(ctx context.Context, r WorkIQDocumentRecord) (w WorkIQDocumentWrite, err error) {
	if !validDocumentRecord(r) {
		return w, errors.New("invalid WorkIQ document record")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	ok, err := hasWorkIQDocuments(ctx, tx)
	if err == nil && !ok {
		err = errors.New("archive has no WorkIQ document tables")
	}
	if err != nil {
		return w, err
	}
	c := r.Check
	at := c.CheckedAt.UTC().Format(time.RFC3339Nano)
	var encrypted any
	if c.LabelEncrypted != nil {
		encrypted = map[bool]int{false: 0, true: 1}[*c.LabelEncrypted]
	}
	admitted := c.Outcome == "admitted"
	if err = tx.QueryRowContext(ctx, `insert into workiq_documents(account_id,drive_id,item_id,name,web_url,first_checked_at,last_checked_at)
values(?,?,?,?,?,?,?) on conflict(account_id,drive_id,item_id) do update set name=excluded.name,web_url=excluded.web_url,last_checked_at=excluded.last_checked_at
returning id`, r.Account, r.DriveID, r.ItemID, r.Name, r.WebURL, at, at).Scan(&w.DocumentID); err != nil {
		return w, err
	}
	if err = tx.QueryRowContext(ctx, `insert into workiq_document_checks(document_id,checked_at,outcome,code,content_protection,label_id,label_name,
label_encrypted,attribution,bytes_state,rights_source,qualification) values(?,?,?,?,?,?,?,?,?,?,?,?) returning id`,
		w.DocumentID, at, c.Outcome, c.Code, c.ContentProtection, c.LabelID, c.LabelName, encrypted, c.Attribution, c.BytesState,
		c.RightsSource, c.Qualification).Scan(&w.CheckID); err != nil {
		return w, err
	}
	res, err := tx.ExecContext(ctx, `delete from workiq_document_bodies where document_id=?`, w.DocumentID)
	if err != nil {
		return w, err
	}
	deleted, _ := res.RowsAffected()
	w.BodyDeleted = !admitted && deleted > 0
	if deleted > 0 {
		// Older page images of the removed text are cleared from the write-ahead log after commit.
		if err = markPurgedLog(ctx, tx); err != nil {
			return w, err
		}
	}
	var body any
	if admitted {
		body = w.CheckID
		if _, err = tx.ExecContext(ctx, `insert into workiq_document_bodies(document_id,check_id,text,digest,extracts,truncated) values(?,?,?,?,?,?)`,
			w.DocumentID, w.CheckID, r.Text, documentDigest(r.Text), r.Extracts, map[bool]int{false: 0, true: 1}[r.Truncated]); err != nil {
			return w, err
		}
	}
	if _, err = tx.ExecContext(ctx, `update workiq_documents set last_check_id=?,body_check_id=? where id=?`, w.CheckID, body, w.DocumentID); err != nil {
		return w, err
	}
	if err = tx.Commit(); err != nil || deleted == 0 {
		return w, err
	}
	afterWorkIQDocumentCommit()
	// The check is committed, so a caller's deadline must not turn this into a retried write; the
	// clear is bounded by its own attempts, and a blocked one stays pending for the next sync.
	log, err := s.ClearPurgedLog(context.WithoutCancel(ctx))
	w.LogPending = log.Pending
	return w, err
}

const workiqDocumentSelect = `select d.id,d.account_id,d.drive_id,d.item_id,d.name,d.web_url,d.first_checked_at,d.last_checked_at,
c.id,c.checked_at,c.outcome,c.code,c.content_protection,c.label_id,c.label_name,c.label_encrypted,c.attribution,c.bytes_state,
c.rights_source,c.qualification,b.check_id,b.text,b.digest,b.extracts,b.truncated
from workiq_documents d join workiq_document_checks c on c.id=d.last_check_id
left join workiq_document_bodies b on b.document_id=d.id and b.check_id=d.body_check_id`

func scanWorkIQDocument(row interface{ Scan(...any) error }) (WorkIQDocument, error) {
	var d WorkIQDocument
	var first, last, checked string
	var encrypted, bodyCheck, extracts, truncated sql.NullInt64
	var text, digest sql.NullString
	err := row.Scan(&d.ID, &d.Account, &d.DriveID, &d.ItemID, &d.Name, &d.WebURL, &first, &last,
		&d.Last.ID, &checked, &d.Last.Outcome, &d.Last.Code, &d.Last.ContentProtection, &d.Last.LabelID, &d.Last.LabelName, &encrypted,
		&d.Last.Attribution, &d.Last.BytesState, &d.Last.RightsSource, &d.Last.Qualification,
		&bodyCheck, &text, &digest, &extracts, &truncated)
	var err1, err2, err3 error
	d.FirstCheckedAt, err1 = time.Parse(time.RFC3339Nano, first)
	d.LastCheckedAt, err2 = time.Parse(time.RFC3339Nano, last)
	d.Last.CheckedAt, err3 = time.Parse(time.RFC3339Nano, checked)
	d.Last.LabelEncrypted = nullBool(encrypted)
	if bodyCheck.Valid {
		d.Body = &WorkIQDocumentBody{CheckID: bodyCheck.Int64, Text: text.String, Digest: digest.String, Extracts: int(extracts.Int64), Truncated: truncated.Int64 == 1}
		if d.Last.Outcome != "admitted" || digest.String != documentDigest(text.String) {
			err3 = errors.Join(err3, errors.New("WorkIQ document body does not match its check"))
		}
	}
	return d, errors.Join(err, err1, err2, err3)
}

func nullBool(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 == 1
	return &b
}

// workIQDocumentRows reads documents; a schema below 23 reads as none.
func (s *Store) workIQDocumentRows(ctx context.Context, tail string, args ...any) ([]WorkIQDocument, error) {
	ok, err := hasWorkIQDocuments(ctx, s.db)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, workiqDocumentSelect+tail, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []WorkIQDocument
	for rows.Next() {
		d, err := scanWorkIQDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// WorkIQDocuments lists every fetched document with its latest check, newest check first.
func (s *Store) WorkIQDocuments(ctx context.Context) ([]WorkIQDocument, error) {
	return s.workIQDocumentRows(ctx, ` order by d.last_checked_at desc,d.id desc`)
}

// WorkIQDocumentDetail reads one document and its checks, newest first, at most limit of them.
func (s *Store) WorkIQDocumentDetail(ctx context.Context, id int64, limit int) (WorkIQDocument, []WorkIQDocumentCheck, error) {
	docs, err := s.workIQDocumentRows(ctx, ` where d.id=?`, id)
	if err == nil && len(docs) == 0 {
		err = ErrWorkIQDocumentUnknown
	}
	if err != nil {
		return WorkIQDocument{}, nil, err
	}
	checks, err := s.workIQDocumentChecks(ctx, id, limit)
	return docs[0], checks, err
}

func (s *Store) workIQDocumentChecks(ctx context.Context, id int64, limit int) ([]WorkIQDocumentCheck, error) {
	rows, err := s.db.QueryContext(ctx, `select id,checked_at,outcome,code,content_protection,label_id,label_name,label_encrypted,attribution,
bytes_state,rights_source,qualification from workiq_document_checks where document_id=? order by id desc limit ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var checks []WorkIQDocumentCheck
	for rows.Next() {
		var c WorkIQDocumentCheck
		var at string
		var encrypted sql.NullInt64
		err := rows.Scan(&c.ID, &at, &c.Outcome, &c.Code, &c.ContentProtection, &c.LabelID, &c.LabelName, &encrypted,
			&c.Attribution, &c.BytesState, &c.RightsSource, &c.Qualification)
		var parseErr error
		c.CheckedAt, parseErr = time.Parse(time.RFC3339Nano, at)
		if err = errors.Join(err, parseErr); err != nil {
			return nil, err
		}
		c.LabelEncrypted = nullBool(encrypted)
		checks = append(checks, c)
	}
	return checks, rows.Err()
}

// SearchWorkIQDocuments returns admitted documents whose retained title or text match an FTS5
// query, best match first.
func (s *Store) SearchWorkIQDocuments(ctx context.Context, query string, limit int) ([]WorkIQDocument, error) {
	match := buildFTSQuery(query)
	if match == "" {
		return nil, errs.Usage("content search requires searchable words")
	}
	return s.workIQDocumentRows(ctx, ` join workiq_document_fts f on f.rowid=d.id where workiq_document_fts match ? order by f.rank,d.id limit ?`, match, limit)
}
