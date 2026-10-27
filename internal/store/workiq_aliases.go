package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// mailPublicAlias resolves only published aliases, never an incoming native or Graph key.
func (s *Store) mailPublicAlias(ctx context.Context, account string, key uint32) (int64, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return 0, err
	}
	var rowid int64
	err = s.db.QueryRowContext(ctx, `select m.rowid from workiq_aliases a
left join mail_messages m on m.rowid=a.item_rowid and m.account=a.account_id
where a.kind='mail' and a.account_id=? and a.namespace='public' and a.alias=?`,
		account, strconv.FormatUint(uint64(key), 10)).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rowid, err
}

func bindWorkIQMailAlias(ctx context.Context, tx *sql.Tx, account, namespace, alias string, rowid int64) error {
	if account == "" || alias == "" || len(alias) > 64*1024 {
		return errors.New("invalid mail source alias")
	}
	switch namespace {
	case "public", "native_detail":
		key, err := strconv.ParseUint(alias, 10, 32)
		if err != nil {
			return errors.New("invalid numeric mail source alias")
		}
		alias = strconv.FormatUint(key, 10)
	case "graph", "native_owa":
	default:
		return errors.New("invalid mail source alias namespace")
	}
	var canonical int64
	if err := tx.QueryRowContext(ctx, `select rowid from mail_messages where account=? and rowid=?`, account, rowid).Scan(&canonical); err != nil {
		return err
	}
	if namespace == "public" {
		var existing int64
		err := tx.QueryRowContext(ctx, `select rowid from mail_messages where account=? and detail_key=?`, account, alias).Scan(&existing)
		switch {
		case err == nil && existing != canonical:
			return errors.New("mail alias would steal a canonical ID")
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail',?,?,?,?)
on conflict(kind,account_id,namespace,alias) do nothing`, account, namespace, alias, canonical); err != nil {
		return err
	}
	var existing int64
	if err := tx.QueryRowContext(ctx, `select item_rowid from workiq_aliases where kind='mail' and account_id=? and namespace=? and alias=?`,
		account, namespace, alias).Scan(&existing); err != nil {
		return err
	}
	if existing != canonical {
		return errors.New("mail source alias is already bound to another row")
	}
	return nil
}
