package teamsdesktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/indexeddb"
	"github.com/ourostack/teamscrawl/internal/leveldb"
	"github.com/ourostack/teamscrawl/internal/v8"
)

// typed is the set of databases that have a mapper, and so are read by Read: manager -> (object
// store, kind). Every other database that is not Denied is read generically by ReadGeneric.
var typed = map[string]struct{ store, kind string }{
	"replychain-manager":   {"replychains-2", KindReplyChain},
	"conversation-manager": {"conversations", KindConversation},
	"activity-manager":     {"feed-items", KindActivity},
}

// Omission code names added by this package, beside the indexeddb ones.
const (
	omitTruncatedLogTail = "truncated_log_tail"
	omitUnmappedRecord   = "unmapped_record"
)

// origin is the part of *indexeddb.Origin that Read uses (a seam for tests).
type origin interface {
	Databases() ([]indexeddb.Database, error)
	Records(dbID, storeID int64, fn func(indexeddb.Record) error) error
	Decode(dbID int64, raw []byte) (any, error)
	Stats() leveldb.Stats
	Payload(dbID int64, raw []byte) ([]byte, error)
	DecodePayload(payload []byte) (any, error)
}

// ReadOptions tunes ReadWith.
type ReadOptions struct {
	// Sig is the signature (see MemoSignature) that record digests are taken under.
	Sig []byte
	// Skip, when set, is called for every record whose key decoded, before its value is decoded,
	// with the record's account, kind, database, canonical key and digest. digest is nil when the
	// key or the value's bytes could not be read, and then Skip must report false. When Skip
	// reports true the record is not decoded, mapped or passed to fn: the caller has already done
	// what fn would have done, with the same outcome. Skip must report true only when that holds.
	Skip func(acct Account, kind, database, keyJSON string, digest []byte) bool
}

// Read opens the snapshot in snapDir and calls fn for every record of the typed stores
// (replychain-manager/replychains-2, conversation-manager/conversations,
// activity-manager/feed-items), optionally only for one account (matched on tenant and user).
// Records that cannot be decoded are omissions, counted by code in the returned map, as are keys
// that fail to decode (bad_key), records fn rejects with *UnmappedError (unmapped_record) and
// truncated log tails (truncated_log_tail). A typed manager that is absent is skipped; one
// that is present without its store is store_missing. Other errors from fn stop the read and are
// returned as is.
func Read(ctx context.Context, snapDir string, account *Account, fn func(acct Account, kind string, v any) error) (omissions map[string]int, err error) {
	return ReadWith(ctx, snapDir, account, ReadOptions{}, fn)
}

// ReadWith is Read with options.
func ReadWith(ctx context.Context, snapDir string, account *Account, opts ReadOptions, fn func(acct Account, kind string, v any) error) (omissions map[string]int, err error) {
	o, err := indexeddb.OpenWith(filepath.Join(snapDir, "leveldb"), filepath.Join(snapDir, "blob"),
		indexeddb.OpenOptions{KeepDatabase: keepDatabase(account)})
	if err != nil {
		// Cancelling removes the snapshot while its tables load, so the failure can look like a
		// damaged cache: report the cancellation instead.
		if cerr := ctx.Err(); cerr != nil {
			return map[string]int{}, cerr
		}
		return map[string]int{}, classify(err)
	}
	defer func() { _ = o.Close() }()
	return readOriginWith(ctx, o, account, opts, fn)
}

// keepDatabase selects the databases Read loads into memory: the typed managers, for the
// chosen account when there is one. readOrigin applies the same rule again when it walks them.
func keepDatabase(account *Account) func(name string) bool {
	return func(name string) bool {
		manager, acct, ok := ParseDatabaseName(name)
		if !ok {
			return false
		}
		if _, isTyped := typed[manager]; !isTyped {
			return false
		}
		return account == nil || (account.TenantID == acct.TenantID && account.UserID == acct.UserID)
	}
}

func readOrigin(ctx context.Context, o origin, account *Account, fn func(acct Account, kind string, v any) error) (map[string]int, error) {
	return readOriginWith(ctx, o, account, ReadOptions{}, fn)
}

func readOriginWith(ctx context.Context, o origin, account *Account, opts ReadOptions, fn func(acct Account, kind string, v any) error) (map[string]int, error) {
	omissions := map[string]int{}
	dbs, err := o.Databases()
	if err != nil {
		return omissions, classify(err)
	}
	for _, db := range dbs {
		manager, acct, ok := ParseDatabaseName(db.Name)
		if !ok {
			continue
		}
		spec, isTyped := typed[manager]
		if !isTyped {
			continue
		}
		if account != nil && (account.TenantID != acct.TenantID || account.UserID != acct.UserID) {
			continue
		}
		var storeID int64 = -1
		for _, s := range db.Stores {
			if s.Name == spec.store {
				storeID = s.ID
				break
			}
		}
		if storeID < 0 {
			return omissions, errs.StoreMissing(fmt.Sprintf("%s has no %s store", "Teams:"+manager+":", spec.store))
		}
		err := o.Records(db.ID, storeID, func(r indexeddb.Record) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if r.Err != nil {
				return count(omissions, r.Err)
			}
			// The value is unwrapped (snappy, blob file) once: the payload that is digested is the
			// one that is decoded.
			var payload []byte
			if opts.Skip != nil {
				var keyJSON string
				var digest []byte
				if kj, err := v8.Canonical(canonKey(r.Key)); err == nil {
					keyJSON = string(kj)
					var perr error
					if payload, perr = o.Payload(db.ID, r.Raw); perr == nil {
						digest = recordDigest(opts.Sig, db.Name, payload)
					} else {
						payload = nil
					}
				}
				if opts.Skip(acct, spec.kind, db.Name, keyJSON, digest) {
					return nil
				}
			}
			v, err := decodeRecord(o, db.ID, r.Raw, payload)
			if err != nil {
				return count(omissions, err)
			}
			if err := fn(acct, spec.kind, v); err != nil {
				var um *UnmappedError
				if errors.As(err, &um) {
					omissions[omitUnmappedRecord]++
					return nil
				}
				return &callbackError{err}
			}
			return nil
		})
		if err != nil {
			// Cancelling removes the snapshot, so a value re-read after it fails as a missing
			// file: report the cancellation instead.
			if cerr := ctx.Err(); cerr != nil {
				return omissions, cerr
			}
			return omissions, classifyRead(err)
		}
	}
	if n := o.Stats().TruncatedLogTails; n > 0 {
		omissions[omitTruncatedLogTail] += n
	}
	return omissions, nil
}

// decodeRecord decodes a record value, from its payload when the caller already unwrapped it
// (payload is nil otherwise).
func decodeRecord(o interface {
	Decode(dbID int64, raw []byte) (any, error)
	DecodePayload(payload []byte) (any, error)
}, dbID int64, raw, payload []byte) (any, error) {
	if payload != nil {
		return o.DecodePayload(payload)
	}
	return o.Decode(dbID, raw)
}

// count records an omission. Any other error is fatal and returned.
func count(omissions map[string]int, err error) error {
	var om *indexeddb.OmissionError
	if errors.As(err, &om) {
		omissions[om.Code]++
		return nil
	}
	return err
}

// callbackError marks an error returned by the caller's fn so it is passed back untouched.
type callbackError struct{ err error }

func (e *callbackError) Error() string { return e.err.Error() }
func (e *callbackError) Unwrap() error { return e.err }

// classifyRead returns callback and cancellation errors as they are and codes reader errors.
func classifyRead(err error) error {
	var cb *callbackError
	if errors.As(err, &cb) {
		return cb.err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return classify(err)
}

// classify maps reader errors to coded ones.
func classify(err error) error {
	var missing *leveldb.MissingFileError
	switch {
	case errors.Is(err, leveldb.ErrUnsupportedCompression):
		return errs.UnsupportedBlockCompression(err.Error())
	case errors.As(err, &missing), errors.Is(err, leveldb.ErrManifestTruncated):
		return errs.SnapshotInconsistent(err.Error())
	default:
		return errs.DBError(err)
	}
}
