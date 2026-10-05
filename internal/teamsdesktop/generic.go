package teamsdesktop

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

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
	// omitDatabaseUnreadable is a database ReadGeneric could not read (data loss, unlike a denial).
	omitDatabaseUnreadable = "database_unreadable"
)

// GenericRecord is one record of a database that has no typed mapper. Database is the full
// IndexedDB name. KeyJSON is v8.Canonical of the decoded key. ValueJSON is nil when the value
// failed to decode: the key still counts as seen. Account is nil when the name does not parse.
type GenericRecord struct {
	Account            *Account
	Database, Store    string
	KeyJSON, ValueJSON []byte
	// Digest is the read memory's digest of the bytes ValueJSON was made from (see MemoSignature),
	// and ValueRedacted how many redactions Scrub made in the value. Digest is nil unless the
	// value decoded and encoded cleanly, so a record that is an omission is never remembered.
	Digest        []byte
	ValueRedacted int
}

// GenericResult describes a ReadGeneric pass.
type GenericResult struct {
	// Omissions counts omissions by code, including denied_database, denied_store and
	// database_unreadable.
	Omissions map[string]int
	// Present lists every database in scope that has something to read: every non-typed,
	// non-denied database, and every typed one that has a store its mapper does not consume.
	Present []string
	// Complete says, per database, that every store was read with no fatal error and no bad_key.
	Complete map[string]bool
	// Redacted counts the credential-looking fragments Scrub removed from values.
	Redacted int
}

// GenericOptions tunes ReadGeneric.
type GenericOptions struct {
	// OnDatabase, when set, is called once per database right after it was read, before the next
	// database is read. complete is GenericResult.Complete for it. seen holds, per object store,
	// the KeyJSON of every record seen (decoded or not); it is nil when the database could not be
	// read, and ReadGeneric drops it afterwards, so memory is bounded by one database's keys. An
	// error from OnDatabase stops the read and is returned as is.
	OnDatabase func(db string, complete bool, seen map[string]map[string]struct{}) error
	// Known, when set, is asked about every record whose key and value bytes can be read, before
	// the value is decoded. digest is the record's digest under Sig. When Known reports ok the
	// record is skipped: it is still seen, and redacted (what Known returns) is added to the
	// result's Redacted as if the value had been scrubbed again. Known must report ok only when
	// the archive already holds exactly the row a full read of this record would write. It is not
	// asked about a record whose scrubbed key an earlier record of the same object store already
	// had (they share one row, which that record may have rewritten): such a record is read in full.
	Sig   []byte
	Known func(database, store, keyJSON string, digest []byte) (redacted int, ok bool)
}

// genericOrigin is the part of *indexeddb.Origin that ReadGeneric uses (a seam for tests).
type genericOrigin interface {
	Records(dbID, storeID int64, fn func(indexeddb.Record) error) error
	Decode(dbID int64, raw []byte) (any, error)
	Stats() leveldb.Stats
	Close() error
	Payload(dbID int64, raw []byte) ([]byte, error)
}

// Seams: the census of the snapshot and the opening of one batch's origin, which keeps the
// object stores keep reports (by database id and object store id, so no second metadata pass).
var (
	censusFn  = indexeddb.Census
	openBatch = func(snapDir string, keep func(dbID, storeID int64) bool) (genericOrigin, error) {
		return indexeddb.OpenWith(filepath.Join(snapDir, "leveldb"), filepath.Join(snapDir, "blob"),
			indexeddb.OpenOptions{KeepStore: keep})
	}
)

// genericDB is a database to read and the object stores of it that are read.
type genericDB struct {
	db     indexeddb.Database
	stores []indexeddb.Store
}

// ReadGeneric calls fn for every record of the object stores that are neither consumed by a typed
// reader (see Read: exactly the (manager, store) pairs in typed) nor denied (see Denied), in every
// database that is not denied. Typed databases are included for their other stores. It takes a
// census of the snapshot first, groups the databases greedily, in order, into batches whose
// estimated memory is at most budget (a database larger than the budget is a batch alone; the
// census counts a whole database, so it may over-estimate when stores are excluded), and opens
// the snapshot once per batch with only that batch's kept stores loaded. Each opened origin is
// dropped, and the heap collected, before the next is opened, so memory is bounded by the batch.
// With an account, databases of other accounts and databases whose names do not parse are
// skipped. Omissions are counted by code; a value that cannot be decoded is an omission and its
// key is still seen. A fatal error inside one database (other than cancellation or an error from
// fn) marks that database incomplete, counts database_unreadable and moves on to the next; a
// batch that cannot be opened does so for each of its databases. The callback should write in
// chunks. Errors from fn and cancellation stop the read and are returned as is.
func ReadGeneric(ctx context.Context, snapDir string, account *Account, budget int64, opts GenericOptions, fn func(GenericRecord) error) (GenericResult, error) {
	res := GenericResult{
		Omissions: map[string]int{},
		Complete:  map[string]bool{},
	}
	held, dbs, err := censusFn(filepath.Join(snapDir, "leveldb"))
	if err != nil {
		return res, ctxOr(ctx, classify(err))
	}
	var todo []genericDB
	for _, db := range dbs {
		manager, acct, ok := ParseDatabaseName(db.Name)
		if account != nil && (!ok || account.TenantID != acct.TenantID || account.UserID != acct.UserID) {
			continue
		}
		if Denied(db.Name) {
			res.Omissions[omitDeniedDatabase]++
			continue
		}
		spec, isTyped := typed[manager]
		isTyped = ok && isTyped
		g := genericDB{db: db}
		for _, st := range db.Stores {
			switch {
			case isTyped && st.Name == spec.store:
			case Denied(st.Name):
				res.Omissions[omitDeniedStore]++
			default:
				g.stores = append(g.stores, st)
			}
		}
		if isTyped && len(g.stores) == 0 {
			continue
		}
		todo = append(todo, g)
		res.Present = append(res.Present, db.Name)
	}
	for _, batch := range planBatches(todo, held, budget) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := readBatch(ctx, snapDir, batch, &res, opts, fn); err != nil {
			return res, err
		}
		runtime.GC()
	}
	return res, nil
}

// planBatches groups databases in order into batches of at most budget estimated bytes; a
// database over the budget gets a batch of its own.
func planBatches(dbs []genericDB, held map[int64]int64, budget int64) [][]genericDB {
	var batches [][]genericDB
	var cur []genericDB
	var size int64
	for _, g := range dbs {
		est := held[g.db.ID]
		if len(cur) > 0 && size+est > budget {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, g)
		size += est
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// readBatch opens the snapshot with only the batch's stores kept and reads them. The origin is
// local to this call, so it is garbage once the call returns.
func readBatch(ctx context.Context, snapDir string, batch []genericDB, res *GenericResult, opts GenericOptions, fn func(GenericRecord) error) error {
	keep := map[[2]int64]bool{}
	for _, g := range batch {
		for _, st := range g.stores {
			keep[[2]int64{g.db.ID, st.ID}] = true
		}
	}
	o, err := openBatch(snapDir, func(dbID, storeID int64) bool { return keep[[2]int64{dbID, storeID}] })
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		for _, g := range batch {
			if err := unreadable(res, opts, g.db.Name); err != nil {
				return err
			}
		}
		return nil
	}
	defer func() { _ = o.Close() }()
	for _, g := range batch {
		complete, seen, err := readGenericDatabase(ctx, o, g, res, opts, fn)
		if err != nil {
			var cb *callbackError
			// Cancelling removes the snapshot, so a value re-read after it fails as a missing
			// file: report the cancellation instead.
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if errors.As(err, &cb) {
				return cb.err
			}
			if err := unreadable(res, opts, g.db.Name); err != nil {
				return err
			}
			continue
		}
		res.Complete[g.db.Name] = complete
		if opts.OnDatabase != nil {
			if err := opts.OnDatabase(g.db.Name, complete, seen); err != nil {
				return err
			}
		}
	}
	if n := o.Stats().TruncatedLogTails; n > res.Omissions[omitTruncatedLogTail] {
		res.Omissions[omitTruncatedLogTail] = n
	}
	return nil
}

// unreadable records that a database could not be read and tells the OnDatabase callback.
func unreadable(res *GenericResult, opts GenericOptions, name string) error {
	res.Omissions[omitDatabaseUnreadable]++
	res.Complete[name] = false
	if opts.OnDatabase != nil {
		return opts.OnDatabase(name, false, nil)
	}
	return nil
}

func readGenericDatabase(ctx context.Context, o genericOrigin, g genericDB, res *GenericResult, opts GenericOptions, fn func(GenericRecord) error) (bool, map[string]map[string]struct{}, error) {
	db := g.db
	var acct *Account
	if _, a, ok := ParseDatabaseName(db.Name); ok {
		acct = &a
	}
	seenStores := map[string]map[string]struct{}{}
	complete := true
	for _, st := range g.stores {
		seen := map[string]struct{}{}
		seenStores[st.Name] = seen
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
			keyJSON, kn := Scrub(keyJSON)
			res.Redacted += kn
			// Scrubbing can map several keys to one row. Known vouches for a row as the archive
			// stood before this read, and an earlier record of this store that maps to the same row
			// may have changed it since, so only the first record of a row may be skipped. Every
			// later one is read in full, in order, so the row ends where a full read leaves it.
			_, shared := seen[string(keyJSON)]
			seen[string(keyJSON)] = struct{}{}
			var digest []byte
			if opts.Known != nil {
				if payload, perr := o.Payload(db.ID, r.Raw); perr == nil { // an unreadable value is read in full below, as an omission
					digest = recordDigest(opts.Sig, db.Name, payload)
					if !shared {
						if red, ok := opts.Known(db.Name, st.Name, string(keyJSON), digest); ok {
							res.Redacted += red
							return nil
						}
					}
				}
			}
			rec := GenericRecord{Account: acct, Database: db.Name, Store: st.Name, KeyJSON: keyJSON}
			if v, err := o.Decode(db.ID, r.Raw); err != nil {
				if cerr := count(res.Omissions, err); cerr != nil {
					return cerr
				}
			} else if rec.ValueJSON, err = v8.Canonical(v); err != nil {
				rec.ValueJSON = nil
				res.Omissions[omitUnencodableValue]++
			} else {
				var n int
				rec.ValueJSON, n = Scrub(rec.ValueJSON)
				res.Redacted += n
				rec.Digest, rec.ValueRedacted = digest, n
			}
			if err := fn(rec); err != nil {
				return &callbackError{err}
			}
			return nil
		})
		if err != nil {
			return false, nil, err
		}
	}
	return complete, seenStores, nil
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
