package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// WorkIQMailFolderCount requires a retained Graph binding, never a name match.
func (s *Store) WorkIQMailFolderCount(ctx context.Context, account, folder string) (*int, error) {
	if account == "" || folder == "" {
		return nil, errors.New("invalid mail coverage scope")
	}
	present, err := s.hasWorkIQ(ctx)
	if err != nil || !present {
		return nil, err
	}
	var key int64
	err = s.db.QueryRowContext(ctx, `select folder_key from workiq_folders where account_id=? and graph_id=?`, account, folder).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from mail_messages where account=? and folder_key=?
and gone_at is null and evicted_at is null`, account, key).Scan(&count); err != nil {
		return nil, err
	}
	return &count, nil
}

// WorkIQChatCoverageCount counts the archive's documented comparable classes.
func (s *Store) WorkIQChatCoverageCount(ctx context.Context, account workiq.AccountFact, chat string, start, end time.Time) (int, error) {
	if account.Family != "chats" || account.TenantID == "" || account.UserID == "" ||
		account.ID != account.TenantID+"/"+account.UserID || chat == "" || start.IsZero() || end.Before(start) {
		return 0, errors.New("invalid chat coverage scope")
	}
	var count int
	err := s.each(ctx, `select sent_at from messages where tenant_id=? and user_id=? and conversation_id=?
and deleted_at is null
and message_type in ('RichText/Html','Text','RichText/Media_Card','RichText/Media_CallRecording')`,
		func(rows *sql.Rows) error {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			at, err := workIQStoredTime(sql.NullString{String: raw, Valid: true})
			if err != nil {
				return err
			}
			if !at.Before(start) && !at.After(end) {
				count++
			}
			return nil
		}, account.TenantID, account.UserID, chat)
	if err != nil {
		return 0, err
	}
	return count, err
}

func (s *Store) workIQMailCoverage(ctx context.Context) ([]CoverageRow, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	sets, err := loadMailSets(ctx, tx)
	if err != nil {
		return nil, err
	}
	var version int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return nil, err
	}
	query := `select c.account,c.folder_key,coalesce(f.name,''),coalesce(f.kind,'unknown'),
c.oldest_at,c.newest_at,c.count,c.read_at,'',NULL
from mail_coverage c left join mail_folders f on f.account=c.account and f.folder_key=c.folder_key
order by c.account,c.folder_key`
	if version >= WorkIQSchemaVersion {
		query = `with source_accounts as (
select account_id,min(case when cache_seen in (0,1) and server_seen in (0,1) and cache_seen+server_seen>0 then 1 else 0 end) as valid
from workiq_rows where kind='mail' group by account_id),
keys as (select account,folder_key from mail_coverage union
select m.account,m.folder_key from mail_messages m join source_accounts s on s.account_id=m.account)
select k.account,k.folder_key,coalesce(f.name,''),coalesce(f.kind,'unknown'),c.oldest_at,c.newest_at,
case when s.valid=0 then NULL when s.account_id is null then c.count else
(select count(*) from mail_messages m where m.account=k.account and m.folder_key=k.folder_key and m.gone_at is null and m.evicted_at is null) end,
c.read_at,case when s.account_id is null then '' else 'canonical_archive' end,
case when s.account_id is null then NULL else c.count end
from keys k left join mail_coverage c on c.account=k.account and c.folder_key=k.folder_key
left join mail_folders f on f.account=k.account and f.folder_key=k.folder_key
left join source_accounts s on s.account_id=k.account order by k.account,k.folder_key`
	}
	out := []CoverageRow{}
	if err := mailEach(ctx, tx, query, nil, func(r *sql.Rows) error {
		var c CoverageRow
		var oldest, newest, read sql.NullString
		var cached sql.NullInt64
		if err := r.Scan(&c.Account, &c.FolderKey, &c.Folder, &c.Kind, &oldest, &newest, &c.Count, &read, &c.CountBasis, &cached); err != nil {
			return err
		}
		c.OldestAt, c.NewestAt, c.ReadAt = parseTime(oldest), parseTime(newest), parseTime(read)
		c.Current = sets.isCurrent(c.Account, c.FolderKey)
		if cached.Valid {
			n := int(cached.Int64)
			c.CacheCount = &n
		}
		out = append(out, c)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
