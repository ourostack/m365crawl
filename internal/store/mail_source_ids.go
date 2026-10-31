package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
)

const maxMailSourceKey int64 = 1<<32 - 1

// mailSourceKey allocates inside the caller's mail transaction, including keys a Mac read
// already owns. Source ids remain exact strings; only the assigned integer enters common mail.
func mailSourceKey(ctx context.Context, tx *sql.Tx, account, kind, sourceID string, floors ...uint32) (uint32, error) {
	if account == "" || sourceID == "" {
		return 0, errors.New("mail source identity requires an account and a nonempty source id")
	}
	var query string
	var args []any
	registry := "mail_source_ids"
	namespace := ""
	if rest, server := strings.CutPrefix(kind, "workiq:"); server {
		registry, kind = "workiq_source_keys", rest
		namespace = "graph"
	} else if rest, native := strings.CutPrefix(kind, "workiq-native:"); native {
		registry, kind, namespace = "workiq_source_keys", rest, "native"
	}
	switch kind {
	case "message":
		query = `select detail_key as k from mail_messages where account=?
union all select detail_key from mail_absent where account=?
union all select case when alias=cast(cast(alias as integer) as text)
and cast(alias as integer) between 0 and 4294967295 then cast(alias as integer) else -1 end
from workiq_aliases where kind='mail' and account_id=? and namespace='public'`
		args = []any{account, account, account}
	case "folder":
		query = `select folder_key as k from mail_folders where account=?
union all select parent_key from mail_folders where account=?
union all select folder_key from mail_messages where account=?
union all select folder_key from mail_coverage where account=?`
		args = []any{account, account, account, account}
	case "attachment":
		query = `select a.attachment_key as k from mail_attachments a join mail_messages m on m.rowid=a.message_rowid where m.account=?`
		args = []any{account}
	default:
		return 0, errors.New("unknown mail source identity kind")
	}
	var stored int64
	where := "account=? and kind=? and source_id=?"
	lookup := []any{account, kind, sourceID}
	if namespace != "" {
		where += " and namespace=?"
		lookup = append(lookup, namespace)
	}
	err := tx.QueryRowContext(ctx, `select case when typeof(key)='integer' and key between 1 and 4294967295 then key else 0 end
from `+registry+` where `+where, lookup...).Scan(&stored) //nolint:gosec // registry is one of two fixed table names
	switch {
	case err == nil:
		if stored == 0 {
			return 0, errors.New("invalid stored mail source identity key")
		}
		return uint32(stored), nil //nolint:gosec // the SQL expression bounds this integer to uint32
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	query += ` union all select key from mail_source_ids where account=? and kind=?
union all select key from workiq_source_keys where account=? and kind=?`
	args = append(args, account, kind, account, kind)
	var high, invalid int64
	if err := tx.QueryRowContext(ctx, `select
coalesce(max(case when typeof(k)='integer' and k between 0 and 4294967295 then k else 0 end),0),
coalesce(sum(case when typeof(k)='integer' and k between 0 and 4294967295 then 0 else 1 end),0)
from (`+query+`)`, args...).Scan(&high, &invalid); err != nil {
		return 0, err
	}
	if invalid != 0 {
		return 0, errors.New("invalid stored mail identity reference")
	}
	if kind == "folder" {
		var root string
		err := tx.QueryRowContext(ctx, `select value from meta where key=?`, mailRootKey+account).Scan(&root)
		switch {
		case err == nil:
			key, err := strconv.ParseUint(root, 10, 32)
			if err != nil {
				return 0, errors.New("invalid stored Outlook mail root key")
			}
			high = max(high, int64(key)) //nolint:gosec // ParseUint bounds key to uint32
		case !errors.Is(err, sql.ErrNoRows):
			return 0, err
		}
	}
	for _, floor := range floors {
		high = max(high, int64(floor))
	}
	if high == maxMailSourceKey {
		return 0, &errs.Coded{
			Code: "outlook_mail_key_exhausted", Exit: errs.ExitEnvironment,
			Message: "the archive has no numeric mail identity keys left for this account and kind",
			Fix:     "Keep the existing archive for reads; use a separate private archive for further syncs.",
		}
	}
	next := high + 1
	columns, values := "account,kind,source_id,key", "?,?,?,?"
	insert := []any{account, kind, sourceID, next}
	if namespace != "" {
		columns += ",namespace"
		values += ",?"
		insert = append(insert, namespace)
	}
	if _, err := tx.ExecContext(ctx, `insert into `+registry+`(`+columns+`) values(`+values+`)`, insert...); err != nil { //nolint:gosec // registry is one of two fixed table names
		return 0, err
	}
	return uint32(next), nil //nolint:gosec // high is validated before incrementing
}
