package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/ourostack/m365crawl/internal/workiq"
)

const workIQMailIndexPrefix = "workiq_mail_index:"

func putWorkIQMailIdentity(ctx context.Context, tx *sql.Tx, account string, rowid int64, header string) error {
	_, err := tx.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail_identity_index',?,?,?,?)
on conflict(kind,account_id,namespace,alias) do nothing`, account, hashOf(workiq.NormalizeMessageID(header)), strconv.FormatInt(rowid, 10), rowid)
	return err
}

func maybeIndexWorkIQMail(ctx context.Context, tx *sql.Tx, account string, rowid int64, header string) error {
	var version int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version < WorkIQSchemaVersion {
		return nil
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_accounts where family='mail' and account_id=?)
or exists(select 1 from meta where key=?)`, account, workIQMailIndexPrefix+account).Scan(&active); err != nil {
		return err
	}
	if !active {
		return nil
	}
	return putWorkIQMailIdentity(ctx, tx, account, rowid, header)
}

func (s *Store) IndexWorkIQMailCandidates(ctx context.Context, attempt, after int64, limit int) (int64, bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return 0, false, err
	}
	if binding.Fact.Family != "mail" || after < 0 || limit < 1 || limit > 128 {
		return 0, false, errors.New("invalid native mail index scope or bound")
	}
	next, complete := after, false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		type entry struct {
			id     int64
			header string
		}
		var entries []entry
		if err := eachRow(ctx, tx, `select rowid,internet_message_id from mail_messages where account=? and rowid>? order by rowid limit ?`,
			[]any{binding.Fact.ID, after, limit}, func(row *sql.Rows) error {
				var item entry
				if err := row.Scan(&item.id, &item.header); err != nil {
					return err
				}
				entries = append(entries, item)
				return nil
			}); err != nil {
			return err
		}
		for _, e := range entries {
			if err := putWorkIQMailIdentity(ctx, tx, binding.Fact.ID, e.id, e.header); err != nil {
				return err
			}
			next = e.id
		}
		complete = len(entries) < limit
		if complete {
			_, err := tx.ExecContext(ctx, `insert into meta(key,value) values(?,?)
on conflict(key) do update set value=excluded.value where value<>excluded.value`, workIQMailIndexPrefix+binding.Fact.ID, strconv.FormatInt(next, 10))
			return err
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return next, complete, nil
}
