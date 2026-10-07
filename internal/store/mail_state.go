package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const (
	// mailReadKey is the meta key prefix of an account's mail read marker: present once a sync
	// has read the account's mail, valued with the time of that read. A changed store skips the
	// read only when it exists, so an archive that never read mail reads it on the next sync.
	mailReadKey = "outlook_mail_read:"
	// mailFailureKey holds the last failed mail read of an account, until a read succeeds.
	mailFailureKey = "outlook_mail_failure:"
)

// MailState is what the archive remembers of one account's mail reads.
type MailState struct {
	Read    bool      // a sync read the account's mail and no later read failed
	ReadAt  time.Time // when, zero when Read is false
	Failure *OutlookFailure
}

// MailState reads the marker and the last failure of the account's mail.
func (s *Store) MailState(ctx context.Context, account string) (MailState, error) {
	var st MailState
	var read, failure sql.NullString
	err := s.db.QueryRowContext(ctx, `select (select value from meta where key=?), (select value from meta where key=?)`,
		mailReadKey+account, mailFailureKey+account).Scan(&read, &failure)
	if err != nil {
		return st, err
	}
	if read.Valid {
		st.Read, st.ReadAt = true, parseTime(read)
	}
	if failure.Valid {
		st.Failure = new(OutlookFailure)
		err = json.Unmarshal([]byte(failure.String), st.Failure)
	}
	return st, err
}

// SetMailRead records that the account's mail was read at the time and forgets a failure.
func (s *Store) SetMailRead(ctx context.Context, account string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `delete from meta where key=?`, mailFailureKey+account); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, mailReadKey+account, at.UTC().Format(timeLayout))
		return err
	})
}

// ClearMailRead removes the account's read marker, so the next sync reads its mail whatever the
// store's fingerprint says. A sync clears it before it commits anything else from a changed store:
// a sync stopped between the calendar and the mail then cannot leave the old marker standing.
func (s *Store) ClearMailRead(ctx context.Context, account string) error {
	_, err := s.db.ExecContext(ctx, `delete from meta where key=?`, mailReadKey+account)
	return err
}

// SetMailFailure records why the account's last mail read failed and clears the read marker, so
// the next sync reads the mail again even when the store did not change.
func (s *Store) SetMailFailure(ctx context.Context, account string, f OutlookFailure) error {
	b, _ := json.Marshal(f) // plain strings and an int
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `delete from meta where key=?`, mailReadKey+account); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, mailFailureKey+account, string(b))
		return err
	})
}

// MailStatus is the archive's mail in numbers, for `status`.
type MailStatus struct {
	Messages int       `json:"messages"` // not gone and not evicted
	Unread   int       `json:"unread"`
	Folders  int       `json:"folders"`
	OldestAt time.Time `json:"oldest_at,omitzero"`
	SyncedAt time.Time `json:"synced_at,omitzero"`
}

// MailStatus counts the archive's live mail and finds when it was last read.
func (s *Store) MailStatus(ctx context.Context) (MailStatus, error) {
	var m MailStatus
	var oldest, synced sql.NullString
	// A read-only archive from before mail (or before the meta table) has no mail tables yet: it
	// holds no mail.
	var tables int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where name in ('mail_messages','mail_folders','meta')`).Scan(&tables); err != nil {
		return MailStatus{}, err
	}
	if tables < 3 {
		return m, nil
	}
	err := s.db.QueryRowContext(ctx, `select
  (select count(*) from mail_messages where gone_at is null and evicted_at is null),
  (select count(*) from mail_messages where gone_at is null and evicted_at is null and is_read=0),
  (select count(*) from mail_folders),
  (select min(received_at) from mail_messages where gone_at is null and evicted_at is null),
  (select max(value) from meta where key like 'outlook\_mail\_read:%' escape '\')`).
		Scan(&m.Messages, &m.Unread, &m.Folders, &oldest, &synced)
	if err != nil {
		return MailStatus{}, err
	}
	m.OldestAt, m.SyncedAt = parseTime(oldest), parseTime(synced)
	return m, nil
}
