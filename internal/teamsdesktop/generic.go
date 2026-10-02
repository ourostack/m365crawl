package teamsdesktop

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/ourostack/teamscrawl/internal/indexeddb"
	"github.com/ourostack/teamscrawl/internal/leveldb"
	"github.com/ourostack/teamscrawl/internal/v8"
)

// DefaultGenericBudget is the default memory budget of one ReadGeneric batch: the estimated
// bytes a batch's databases hold once opened.
const DefaultGenericBudget int64 = 64 << 20

// Omission code names ReadGeneric adds, beside the indexeddb ones.
const (
	omitDeniedDatabase   = "denied_database"
	omitDeniedStore      = "denied_store"
	omitUnencodableValue = "unencodable_value"
)

// GenericRecord is one record of a database that has no typed mapper. Database is the full
// IndexedDB name. KeyJSON is v8.Canonical of the decoded key. ValueJSON is nil when the value
// failed to decode: the key still counts as seen. Account is nil when the name does not parse.
type GenericRecord struct {
	Account            *Account
	Database, Store    string
	KeyJSON, ValueJSON []byte
}

// GenericResult describes a ReadGeneric pass.
type GenericResult struct {
	// Omissions counts omissions by code, including denied_database and denied_store.
	Omissions map[string]int
	// Present lists every non-typed, non-denied database name in the origin, in scope.
	Present []string
	// Complete says, per database, that every store was read with no fatal error and no bad_key.
	Complete map[string]bool
	// Seen holds, per database and store, the KeyJSON of every record seen (decoded or not).
	Seen map[string]map[string]map[string]struct{}
}

// genericOrigin is the part of *indexeddb.Origin that ReadGeneric uses (a seam for tests).
type genericOrigin interface {
	Records(dbID, storeID int64, fn func(indexeddb.Record) error) error
	Decode(dbID int64, raw []byte) (any, error)
	Stats() leveldb.Stats
}

// Seams: the census of the snapshot and the opening of one batch's origin.
var (
	censusFn  = indexeddb.Census
	openBatch = func(snapDir string, keep func(string) bool) (genericOrigin, error) {
		return indexeddb.OpenWith(filepath.Join(snapDir, "leveldb"), filepath.Join(snapDir, "blob"),
			indexeddb.OpenOptions{KeepDatabase: keep})
	}
)

// ReadGeneric calls fn for every record of every database that is neither typed (it has a mapper,
// see Read) nor denied (see Denied), skipping denied object stores. It takes a census of the
// snapshot first, groups the databases greedily, in order, into batches whose estimated memory
// is at most budget (a database larger than the budget is a batch alone), and opens the snapshot
// once per batch with only that batch's databases kept. Each opened origin is dropped before the
// next is opened, so memory is bounded by the batch. With an account, databases of other accounts
// and databases whose names do not parse are skipped. Omissions are counted by code; a value that
// cannot be decoded is an omission and its key is still seen. The callback should write in chunks.
// Errors from fn stop the read and are returned as is.
func ReadGeneric(ctx context.Context, snapDir string, account *Account, budget int64, fn func(GenericRecord) error) (GenericResult, error) {
	res := GenericResult{
		Omissions: map[string]int{},
		Complete:  map[string]bool{},
		Seen:      map[string]map[string]map[string]struct{}{},
	}
	held, dbs, err := censusFn(filepath.Join(snapDir, "leveldb"))
	if err != nil {
		return res, ctxOr(ctx, classify(err))
	}
	var todo []indexeddb.Database
	for _, db := range dbs {
		manager, acct, ok := ParseDatabaseName(db.Name)
		if account != nil && (!ok || account.TenantID != acct.TenantID || account.UserID != acct.UserID) {
			continue
		}
		if Denied(db.Name) {
			res.Omissions[omitDeniedDatabase]++
			continue
		}
		if _, isTyped := typed[manager]; ok && isTyped {
			continue
		}
		todo = append(todo, db)
		res.Present = append(res.Present, db.Name)
	}
	for _, batch := range planBatches(todo, held, budget) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := readBatch(ctx, snapDir, batch, &res, fn); err != nil {
			return res, err
		}
	}
	return res, nil
}

// planBatches groups databases in order into batches of at most budget estimated bytes; a
// database over the budget gets a batch of its own.
func planBatches(dbs []indexeddb.Database, held map[int64]int64, budget int64) [][]indexeddb.Database {
	var batches [][]indexeddb.Database
	var cur []indexeddb.Database
	var size int64
	for _, db := range dbs {
		est := held[db.ID]
		if len(cur) > 0 && size+est > budget {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, db)
		size += est
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// readBatch opens the snapshot with only the batch's databases kept and reads them. The origin
// is local to this call, so it is garbage once the call returns.
func readBatch(ctx context.Context, snapDir string, batch []indexeddb.Database, res *GenericResult, fn func(GenericRecord) error) error {
	names := map[string]bool{}
	for _, db := range batch {
		names[db.Name] = true
	}
	o, err := openBatch(snapDir, func(name string) bool { return names[name] })
	if err != nil {
		return ctxOr(ctx, classify(err))
	}
	for _, db := range batch {
		if err := readGenericDatabase(ctx, o, db, res, fn); err != nil {
			// Cancelling removes the snapshot, so a value re-read after it fails as a missing
			// file: report the cancellation instead.
			return ctxOr(ctx, classifyRead(err))
		}
	}
	if n := o.Stats().TruncatedLogTails; n > res.Omissions[omitTruncatedLogTail] {
		res.Omissions[omitTruncatedLogTail] = n
	}
	return nil
}

func readGenericDatabase(ctx context.Context, o genericOrigin, db indexeddb.Database, res *GenericResult, fn func(GenericRecord) error) error {
	var acct *Account
	if _, a, ok := ParseDatabaseName(db.Name); ok {
		acct = &a
	}
	res.Seen[db.Name] = map[string]map[string]struct{}{}
	complete := true
	for _, st := range db.Stores {
		if Denied(st.Name) {
			res.Omissions[omitDeniedStore]++
			continue
		}
		seen := map[string]struct{}{}
		res.Seen[db.Name][st.Name] = seen
		err := o.Records(db.ID, st.ID, func(r indexeddb.Record) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if r.Err != nil {
				complete = false
				return count(res.Omissions, r.Err)
			}
			keyJSON, err := v8.Canonical(canonKey(r.Key))
			if err != nil {
				complete = false
				res.Omissions[indexeddb.CodeBadKey]++
				return nil
			}
			seen[string(keyJSON)] = struct{}{}
			rec := GenericRecord{Account: acct, Database: db.Name, Store: st.Name, KeyJSON: keyJSON}
			if v, err := o.Decode(db.ID, r.Raw); err != nil {
				if cerr := count(res.Omissions, err); cerr != nil {
					return cerr
				}
			} else if rec.ValueJSON, err = v8.Canonical(v); err != nil {
				rec.ValueJSON = nil
				res.Omissions[omitUnencodableValue]++
			}
			if err := fn(rec); err != nil {
				return &callbackError{err}
			}
			return nil
		})
		if err != nil {
			res.Complete[db.Name] = false
			return err
		}
	}
	res.Complete[db.Name] = complete
	return nil
}

// canonKey converts a decoded IndexedDB key to the value types v8.Canonical writes: binary keys
// become v8.Bytes, array keys convert element by element.
func canonKey(k any) any {
	switch x := k.(type) {
	case []byte:
		return v8.Bytes(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonKey(e)
		}
		return out
	default:
		return k
	}
}

// ctxOr returns the context's error when it is cancelled, else err.
func ctxOr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil && !errors.Is(err, cerr) {
		return cerr
	}
	return err
}
