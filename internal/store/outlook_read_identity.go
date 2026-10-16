package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrAttendeeArchiveCorrupt = errors.New("outlook_attendee_archive_corrupt")

type OutlookReadReceipt struct {
	ID                                        int64
	Account, Origin, Fingerprint, MapperStamp string
	At                                        time.Time
}

type AttendeeOriginState string

func attendeeDigest(parts ...any) string {
	raw, _ := json.Marshal(parts) // only owned, fixed JSON scalar/RawMessage components
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func readReceiptDigest(r OutlookReadReceipt) string {
	return attendeeDigest("outlook-calendar-read-v1", strconv.FormatInt(r.ID, 10), r.Account,
		nullableKnowledge(r.Origin), r.Fingerprint, r.MapperStamp, r.At.Format(time.RFC3339Nano))
}

func hasAttendeeSchema(ctx context.Context, q knowledgeReader) (bool, error) {
	var count, version int
	if err := q.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return false, err
	}
	if version < 19 {
		return false, nil
	}
	if err := q.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in
('outlook_calendar_reads','outlook_calendar_read_state','outlook_link_events','outlook_link_state',
'outlook_attendee_sources','outlook_attendee_requests','outlook_attendee_snapshots',
'outlook_attendee_occurrences','outlook_attendee_bindings','outlook_attendee_members')`).Scan(&count); err != nil {
		return false, err
	}
	if count != 10 {
		return false, ErrAttendeeArchiveCorrupt
	}
	return true, nil
}

func auditAttendeeNamespace(ctx context.Context, q knowledgeReader) error {
	for _, statement := range strings.Split(attendeeTablesDDL, ";") {
		statement = strings.TrimSpace(statement)
		if !strings.HasPrefix(statement, "create table if not exists ") {
			continue
		}
		name := strings.TrimPrefix(statement, "create table if not exists ")
		name = name[:strings.IndexByte(name, '(')]
		var actual string
		if err := q.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name=?`, name).Scan(&actual); err != nil {
			return attendeeIntegrityError(ctx, err)
		}
		expected := strings.Replace(statement, "create table if not exists ", "CREATE TABLE ", 1)
		if actual != expected {
			return ErrAttendeeArchiveCorrupt
		}
	}
	for name, definition := range attendeeTriggerSQL {
		var actual string
		if err := q.QueryRowContext(ctx, `select sql from sqlite_master where type='trigger' and name=?`, name).Scan(&actual); err != nil {
			return attendeeIntegrityError(ctx, err)
		}
		if actual != strings.Replace(definition, " IF NOT EXISTS", "", 1) {
			return ErrAttendeeArchiveCorrupt
		}
	}
	var bad int
	err := q.QueryRowContext(ctx, `select
(select count(*) from (`+knownOutlookAccountsSQL+`) a where not exists(select 1 from outlook_link_state s where s.account_id=a.account_id))
+(select count(*) from outlook_link_state s where s.event_id is not (select max(e.rowid) from outlook_link_events e where e.account_id=s.account_id)
 or (s.event_id is not null and not exists(select 1 from outlook_link_events e where e.rowid=s.event_id and e.account_id=s.account_id)))
+(select count(*) from outlook_calendar_read_state s where not exists(select 1 from outlook_calendar_reads r where r.rowid=s.read_id and r.account_id=s.account_id)
 or s.read_id is not (select max(r.rowid) from outlook_calendar_reads r where r.account_id=s.account_id))
+(select count(*) from (select distinct account_id from outlook_calendar_reads) r where not exists(select 1 from outlook_calendar_read_state s where s.account_id=r.account_id))
+(select count(*) from pragma_foreign_key_check where "table" like 'outlook_%')`).Scan(&bad)
	if err != nil {
		return attendeeIntegrityError(ctx, err)
	}
	if bad != 0 {
		return ErrAttendeeArchiveCorrupt
	}
	return ctx.Err()
}

type attendeeIntegrityFailure struct{ cause error }

func (e *attendeeIntegrityFailure) Error() string        { return ErrAttendeeArchiveCorrupt.Error() }
func (e *attendeeIntegrityFailure) Unwrap() error        { return e.cause }
func (e *attendeeIntegrityFailure) Is(target error) bool { return target == ErrAttendeeArchiveCorrupt }

func attendeeIntegrityError(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &attendeeIntegrityFailure{cause: cause}
}

func checkAttendeeBeforeOpen(ctx context.Context, path string) error {
	db, _ := sql.Open("sqlite", sqliteFileURI(path, "mode=ro"))
	defer func() { _ = db.Close() }()
	var version int
	if err := db.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil || version < 19 {
		return nil
	}
	_, err := hasAttendeeSchema(ctx, db)
	if err != nil {
		return err
	}
	return auditAttendeeNamespace(ctx, db)
}

func ensureAttendeeAccount(ctx context.Context, tx *sql.Tx, account string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from outlook_link_state where account_id=?`, account).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `select count(*) from (`+knownOutlookAccountsSQL+`) where account_id=?`, account).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrAttendeeArchiveCorrupt
	}
	_, err := tx.ExecContext(ctx, `insert into outlook_link_state(account_id,event_id) values(?,null)`, account)
	return err
}

func attendeeLinkRevision(ctx context.Context, q knowledgeReader, account string) (int64, error) {
	if err := auditAttendeeNamespace(ctx, q); err != nil {
		return 0, err
	}
	var revision int64
	if err := q.QueryRowContext(ctx, `select coalesce(event_id,0) from outlook_link_state where account_id=?`, account).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrAttendeeArchiveCorrupt
		}
		return 0, err
	}
	return revision, nil
}

func insertOutlookRead(ctx context.Context, tx *sql.Tx, b OutlookBatch, fingerprint string) (int64, error) {
	r := OutlookReadReceipt{Account: b.Account, Origin: b.Origin, Fingerprint: fingerprint, MapperStamp: b.Stamp, At: b.At}
	result, err := tx.ExecContext(ctx, `insert into outlook_calendar_reads(account_id,origin,fingerprint,mapper_stamp,read_at,receipt_digest) values(?,?,?,?,?,'')`,
		r.Account, nullableKnowledge(r.Origin), r.Fingerprint, r.MapperStamp, r.At.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	r.ID, err = result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `update outlook_calendar_reads set receipt_digest=? where rowid=?`, readReceiptDigest(r), r.ID); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `insert into outlook_calendar_read_state(account_id,read_id) values(?,?) on conflict(account_id) do update set read_id=excluded.read_id`, r.Account, r.ID)
	return r.ID, err
}

func outlookReadReceipt(ctx context.Context, q knowledgeReader, account string) (OutlookReadReceipt, error) {
	var r OutlookReadReceipt
	var origin sql.NullString
	var at, digest string
	err := q.QueryRowContext(ctx, `select r.rowid,r.account_id,r.origin,r.fingerprint,r.mapper_stamp,r.read_at,r.receipt_digest
from outlook_calendar_reads r join outlook_calendar_read_state s on s.account_id=r.account_id and s.read_id=r.rowid where s.account_id=?`, account).
		Scan(&r.ID, &r.Account, &origin, &r.Fingerprint, &r.MapperStamp, &at, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.Origin = origin.String
	r.At, err = time.Parse(time.RFC3339Nano, at)
	if err != nil || at != r.At.Format(time.RFC3339Nano) || digest != readReceiptDigest(r) {
		return OutlookReadReceipt{}, ErrAttendeeArchiveCorrupt
	}
	return r, nil
}

func (s *Store) OutlookReadReceipt(ctx context.Context, account string) (OutlookReadReceipt, error) {
	ok, err := hasAttendeeSchema(ctx, s.db)
	if err != nil || !ok {
		return OutlookReadReceipt{}, err
	}
	if err := auditAttendeeNamespace(ctx, s.db); err != nil {
		return OutlookReadReceipt{}, err
	}
	return outlookReadReceipt(ctx, s.db, account)
}

func (s *Store) CheckAttendeeOrigin(ctx context.Context, account, origin string) (AttendeeOriginState, error) {
	r, err := s.OutlookReadReceipt(ctx, account)
	if err != nil {
		return "", err
	}
	if origin == "" {
		return "unknown", nil
	}
	if r.ID > 0 {
		if r.Origin == "" {
			return "unknown", nil
		}
		if r.Origin == origin {
			return "matched", nil
		}
		return "conflict", nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select
(select count(*) from calendar_sources where source='outlook' and account_id=?)
+(select count(*) from calendar_source_events where source='outlook' and account_id=?)
+(select count(*) from meta where key=?)`, account, account, outlookReadKey+account).Scan(&count); err != nil {
		return "", err
	}
	if count > 0 {
		return "unknown", nil
	}
	return "new", nil
}
