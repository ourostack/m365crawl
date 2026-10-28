package store

import (
	"context"
	"database/sql"
	"errors"
)

// workIQReadColumns derives origin from independent sightings, never the winner
// or opt-in setting. The schema check and content query share the caller's read
// snapshot, including when a legacy archive is migrated by another writer.
func workIQReadColumns(ctx context.Context, tx *sql.Tx, kind string) (string, string, error) {
	var alias, account string
	switch kind {
	case "message":
		alias, account = "m", "m.tenant_id||'/'||m.user_id"
	case "conversation":
		alias, account = "c", "c.tenant_id||'/'||c.user_id"
	case "mail":
		alias, account = "m", "m.account"
	default:
		return "", "", errors.New("unknown source projection kind")
	}
	var version int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0) from schema_migrations`).Scan(&version); err != nil {
		return "", "", err
	}
	if version < 21 {
		return "'desktop_cache'", "NULL", nil
	}
	// All SQL fragments are fixed above. No item or sender-controlled value is
	// interpolated. A corrupt retained flag yields NULL and a scan error, not a
	// made-up desktop origin.
	return `(select case when w.item_rowid is null then 'desktop_cache'
when w.cache_seen=1 and w.server_seen=1 then 'both'
when w.cache_seen=0 and w.server_seen=1 then 'workiq_server'
when w.cache_seen=1 and w.server_seen=0 then 'desktop_cache' end
from (select 1) left join workiq_rows w on w.kind='` + kind + `' and w.account_id=` + account + ` and w.item_rowid=` + alias + `.rowid)`,
		alias + ".deleted_at", nil
}
