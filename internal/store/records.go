package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// UpsertRecords archives generic records by content hash: a new key is inserted, a changed value
// updates its row, an unchanged one only counts. A record with no value (its value failed to
// decode) never overwrites a stored value. Seeing a removed record again clears its removed_at.
func (x *Session) UpsertRecords(source string, rs []teamsdesktop.GenericRecord, at time.Time) (Counts, error) {
	var c Counts
	ctx := context.Background()
	now := fmtTime(at)
	for _, r := range rs {
		var tenant, user string
		if r.Account != nil {
			tenant, user = r.Account.TenantID, r.Account.UserID
		}
		hash := hashOf(string(r.ValueJSON))
		var value any
		if r.ValueJSON != nil {
			value = string(r.ValueJSON)
		}
		c.Seen++
		var oldHash string
		var removed sql.NullString
		err := x.tx.QueryRowContext(ctx, `select content_hash, removed_at from records where source=? and database=? and store=? and key_json=?`,
			source, r.Database, r.Store, string(r.KeyJSON)).Scan(&oldHash, &removed)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := x.tx.ExecContext(ctx, `insert into records(source, tenant_id, user_id, database, store, key_json, value_json, content_hash, first_seen_at, updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
				source, tenant, user, r.Database, r.Store, string(r.KeyJSON), value, hash, now, now); err != nil {
				return c, err
			}
			c.Inserted++
		case err != nil:
			return c, err
		case r.ValueJSON != nil && hash != oldHash:
			if _, err := x.tx.ExecContext(ctx, `update records set value_json=?, content_hash=?, updated_at=?, removed_at=null where source=? and database=? and store=? and key_json=?`,
				value, hash, now, source, r.Database, r.Store, string(r.KeyJSON)); err != nil {
				return c, err
			}
			c.Updated++
		case removed.Valid:
			if _, err := x.tx.ExecContext(ctx, `update records set removed_at=null, updated_at=? where source=? and database=? and store=? and key_json=?`,
				now, source, r.Database, r.Store, string(r.KeyJSON)); err != nil {
				return c, err
			}
			c.Updated++
		default:
			c.Unchanged++
		}
	}
	return c, nil
}

// MarkRecordsRemoved sets removed_at on the live rows of one database of source whose (store, key)
// is not in seen, which maps store to the set of key_json seen (GenericResult.Seen[database]). The
// caller must call it only for a database that was read completely. A row already removed keeps
// its first removal time. It returns how many rows it marked.
func (x *Session) MarkRecordsRemoved(source, database string, seen map[string]map[string]struct{}, at time.Time) (int, error) {
	ctx := context.Background()
	rows, err := x.tx.QueryContext(ctx, `select store, key_json from records where source=? and database=? and removed_at is null`, source, database)
	if err != nil {
		return 0, err
	}
	var gone [][2]string
	for rows.Next() {
		var st, key string
		if err := rows.Scan(&st, &key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if _, ok := seen[st][key]; !ok {
			gone = append(gone, [2]string{st, key})
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	for _, g := range gone {
		if _, err := x.tx.ExecContext(ctx, `update records set removed_at=? where source=? and database=? and store=? and key_json=?`,
			fmtTime(at), source, database, g[0], g[1]); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}

// MarkDatabasesRemoved sets removed_at on the live rows of source whose database is not in present
// (the databases the origin still holds). With an account only that account's rows are touched:
// a filtered read never saw the other accounts' databases. It returns how many rows it marked.
func (x *Session) MarkDatabasesRemoved(source string, present []string, account *teamsdesktop.Account, at time.Time) (int, error) {
	ctx := context.Background()
	q := `select distinct database from records where source=? and removed_at is null`
	args := []any{source}
	if account != nil {
		q += ` and tenant_id=? and user_id=?`
		args = append(args, account.TenantID, account.UserID)
	}
	rows, err := x.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, p := range present {
		have[p] = true
	}
	var gone []string
	for rows.Next() {
		var db string
		if err := rows.Scan(&db); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !have[db] {
			gone = append(gone, db)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	sort.Strings(gone)
	total := 0
	for _, db := range gone {
		res, err := x.tx.ExecContext(ctx, `update records set removed_at=? where source=? and database=? and removed_at is null`, fmtTime(at), source, db)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// PurgeDenied clears credential material from the rows of source whose database or object store
// name now satisfies denied: value_json becomes NULL, content_hash ” and removed_at is set when
// it was not. It is the one place the archive deletes content (a name added to the denylist after
// rows were archived under it) and it is a no-op for rows already cleared. It returns how many
// rows it cleared.
func (x *Session) PurgeDenied(source string, denied func(name string) bool, at time.Time) (int, error) {
	ctx := context.Background()
	rows, err := x.tx.QueryContext(ctx, `select distinct database, store from records where source=? and (value_json is not null or content_hash != '')`, source)
	if err != nil {
		return 0, err
	}
	var hit [][2]string
	for rows.Next() {
		var db, st string
		if err := rows.Scan(&db, &st); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if denied(db) || denied(st) {
			hit = append(hit, [2]string{db, st})
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, h := range hit {
		res, err := x.tx.ExecContext(ctx, `update records set value_json=null, content_hash='', removed_at=coalesce(removed_at, ?) where source=? and database=? and store=? and (value_json is not null or content_hash != '')`,
			fmtTime(at), source, h[0], h[1])
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// PurgeUnscrubbedKeys clears the rows of source whose key_json changes when scrub is applied to
// it (a row archived under a key that held credential material before keys were scrubbed):
// value_json becomes NULL, content_hash ” and removed_at is set when it was not, as PurgeDenied
// does. The record is archived again under its scrubbed key on the next read. It returns how many
// rows it cleared.
func (x *Session) PurgeUnscrubbedKeys(source string, scrub func([]byte) ([]byte, int), at time.Time) (int, error) {
	ctx := context.Background()
	rows, err := x.tx.QueryContext(ctx, `select database, store, key_json from records where source=? and (value_json is not null or content_hash != '')`, source)
	if err != nil {
		return 0, err
	}
	var hit [][3]string
	for rows.Next() {
		var db, st, key string
		if err := rows.Scan(&db, &st, &key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if scrubbed, _ := scrub([]byte(key)); !bytes.Equal(scrubbed, []byte(key)) {
			hit = append(hit, [3]string{db, st, key})
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, h := range hit {
		res, err := x.tx.ExecContext(ctx, `update records set value_json=null, content_hash='', removed_at=coalesce(removed_at, ?) where source=? and database=? and store=? and key_json=?`,
			fmtTime(at), source, h[0], h[1], h[2])
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// StoreRow is one object store of one database, as `stores` lists it. Records counts the live
// rows and Removed the rows no longer in Teams' cache.
type StoreRow struct {
	Database      string    `json:"database"`
	Store         string    `json:"store"`
	Records       int       `json:"records"`
	Removed       int       `json:"removed"`
	LastUpdatedAt time.Time `json:"last_updated_at,omitzero"`
}

// Stores lists every archived database and object store with its row counts, sorted by database
// and store. A nil account means every account.
func (s *Store) Stores(ctx context.Context, account *teamsdesktop.Account) ([]StoreRow, error) {
	var w where
	if account != nil {
		w.add(`tenant_id=? and user_id=?`, account.TenantID, account.UserID)
	}
	rows, err := s.db.QueryContext(ctx, `select database, store, count(*) - count(removed_at), count(removed_at), max(updated_at) from records`+w.sql()+` group by database, store order by database, store`, w.args...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []StoreRow{}
	for rows.Next() {
		var r StoreRow
		var last sql.NullString
		if err := rows.Scan(&r.Database, &r.Store, &r.Records, &r.Removed, &last); err != nil {
			return nil, err
		}
		r.LastUpdatedAt = parseTime(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordFilter selects rows for Records.
type RecordFilter struct {
	Account        *teamsdesktop.Account // nil: every account
	Database       string                // exact name or prefix
	Store          string                // exact name; empty means every store
	Since          time.Time             // only rows updated at or after this time
	IncludeRemoved bool
	Limit          int  // 0 means DefaultLimit
	Total          *int // as Filter.Total
}

// RecordRow is one archived record.
type RecordRow struct {
	Source, TenantID, UserID, Database, Store string
	KeyJSON                                   string
	ValueJSON                                 string // empty when the value never decoded
	FirstSeenAt, UpdatedAt, RemovedAt         time.Time
}

// prefixSuccessor is the smallest string greater than every string that starts with prefix: the
// prefix with its last byte below 0xff incremented and what follows cut off. ok is false when
// every byte is 0xff, so no string is greater than all of them.
func prefixSuccessor(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

// Records lists archived records, newest updated_at first.
func (s *Store) Records(ctx context.Context, f RecordFilter) ([]RecordRow, bool, error) {
	var w where
	if f.Account != nil {
		w.add(`tenant_id=? and user_id=?`, f.Account.TenantID, f.Account.UserID)
	}
	if f.Database != "" {
		if hi, ok := prefixSuccessor(f.Database); ok {
			w.add(`database >= ? and database < ?`, f.Database, hi)
		} else {
			w.add(`database >= ?`, f.Database)
		}
	}
	if f.Store != "" {
		w.add(`store=?`, f.Store)
	}
	if !f.Since.IsZero() {
		w.add(`max(updated_at, coalesce(removed_at, updated_at))>=?`, fmtTime(f.Since))
	}
	if !f.IncludeRemoved {
		w.add(`removed_at is null`)
	}
	limit := (Filter{Limit: f.Limit}).limit()
	rows, err := s.db.QueryContext(ctx, `select source, tenant_id, user_id, database, store, key_json, value_json, first_seen_at, updated_at, removed_at from records`+w.sql()+` order by updated_at desc, database, store, key_json limit ?`, append(w.args, limit+1)...) //nolint:gosec // G202: fragments are package constants; values are placeholders
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := []RecordRow{}
	for rows.Next() {
		var r RecordRow
		var value, first, updated, removed sql.NullString
		if err := rows.Scan(&r.Source, &r.TenantID, &r.UserID, &r.Database, &r.Store, &r.KeyJSON, &value, &first, &updated, &removed); err != nil {
			return nil, false, err
		}
		r.ValueJSON = value.String
		r.FirstSeenAt, r.UpdatedAt, r.RemovedAt = parseTime(first), parseTime(updated), parseTime(removed)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		if err := s.countTotal(ctx, f.Total, ` from records`, &w); err != nil {
			return nil, false, err
		}
		return out[:limit], true, nil
	}
	return out, false, nil
}
