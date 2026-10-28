package store

import (
	"context"
	"database/sql"
)

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
	if version >= 21 {
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
